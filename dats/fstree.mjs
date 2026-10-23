// fstree builds the canonical fixture tree and checks copies of it. The dats suites run it on every host, so it uses node alone.

import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const host = { linux: 'linux', darwin: 'darwin', win32: 'windows' }[process.platform] ?? process.platform;
const posix = host !== 'windows';

function die(msg) {
	console.error(`fstree: ${msg}`);
	process.exit(2);
}

// The fixture is the same bytes on every host: xorshift32 from a fixed seed per file.
function bytes(n, seed) {
	const b = Buffer.alloc(n);
	let x = seed >>> 0 || 1;
	for (let i = 0; i < n; i++) {
		x ^= x << 13;
		x >>>= 0;
		x ^= x >>> 17;
		x ^= x << 5;
		x >>>= 0;
		b[i] = x & 0xff;
	}
	return b;
}

// Every fixture mtime is a whole multiple of 100ns, the NTFS resolution, so every host can hold it exactly.
const baseNs = 981173106_123456700n; // 2001-02-03T04:05:06.1234567Z
function stamp(i) {
	return baseNs + BigInt(i) * 86_400_000_000_000n + BigInt(i) * 1_100n;
}

// node's utimes takes a double, which cannot hold a 100ns step. GNU touch and BSD touch both read this ISO form exactly.
function setMtime(p, ns) {
	const sec = ns / 1_000_000_000n;
	const frac = (ns % 1_000_000_000n).toString().padStart(9, '0');
	const iso = new Date(Number(sec) * 1000).toISOString().replace('.000Z', `.${frac}Z`);
	execFileSync('touch', ['-m', '-d', iso, p]);
}

const files = [
	['empty.bin', 0],
	['one.bin', 1],
	['page-minus-one.bin', 4095],
	['page.bin', 4096],
	['page-plus-one.bin', 4097],
	['big.bin', 4 * 1024 * 1024 + 3],
	['text/hello.txt', 'hello\n'],
	['text/crlf.txt', 'a\r\nb\r\n'],
	['text/no-newline.txt', 'tail'],
	['names/spaced name.txt', 'spaces\n'],
	['names/unicodé-ñ.txt', 'nfc\n'],
	['names/日本語.txt', 'cjk\n'],
	['names/emoji-\u{1f680}.txt', 'emoji\n'],
	['names/.hidden', 'hidden\n'],
	['names/semi;colon & amp.txt', 'punct\n'],
	['deep/a/b/c/d/e/f/g/h/leaf.txt', 'leaf\n'],
	['modes/exec.sh', '#!/bin/sh\necho hi\n'],
	['modes/private.txt', 'private\n'],
	['modes/readonly.txt', 'readonly\n'],
	['hard/a.bin', 70000],
];
const modes = { 'modes/exec.sh': 0o755, 'modes/private.txt': 0o600, 'modes/readonly.txt': 0o444 };
const dirModes = { 'modes/locked': 0o700 };
const emptyDirs = ['empty-dir', 'modes/locked'];

function make(root) {
	if (fs.existsSync(root)) die(`make: ${root} already exists`);
	fs.mkdirSync(root, { recursive: true });
	let i = 0;
	const stamped = [];
	for (const [rel, spec] of files) {
		const p = path.join(root, rel);
		fs.mkdirSync(path.dirname(p), { recursive: true });
		fs.writeFileSync(p, typeof spec === 'number' ? bytes(spec, i + 1) : spec);
		stamped.push([p, stamp(i++)]);
	}
	// A sparse file: a hole, one byte, then a hole to the end.
	const sparse = path.join(root, 'sparse.bin');
	const fd = fs.openSync(sparse, 'w');
	fs.ftruncateSync(fd, 16 * 1024 * 1024);
	fs.writeSync(fd, Buffer.from('x'), 0, 1, 8 * 1024 * 1024);
	fs.closeSync(fd);
	stamped.push([sparse, stamp(i++)]);
	fs.linkSync(path.join(root, 'hard/a.bin'), path.join(root, 'hard/b.bin'));
	for (const d of emptyDirs) fs.mkdirSync(path.join(root, d), { recursive: true });
	fs.symlinkSync('text/hello.txt'.split('/').join(path.sep), path.join(root, 'link-file'), 'file');
	fs.symlinkSync('deep', path.join(root, 'link-dir'), 'dir');
	fs.symlinkSync('no-such-target', path.join(root, 'link-dangling'), 'file');
	// Times before modes: a read-only file on NTFS still takes a new time, but this order needs no such guarantee.
	for (const [p, ns] of stamped) setMtime(p, ns);
	for (const [rel, m] of Object.entries(modes)) fs.chmodSync(path.join(root, rel), m);
	// Directories last and deepest first, so creating a child never moves a parent's stamp.
	const dirs = [];
	walk(root, '', (rel, st) => st.isDirectory() && dirs.push(rel));
	dirs.sort((a, b) => b.split('/').length - a.split('/').length || (a < b ? -1 : 1));
	for (const rel of dirs) setMtime(path.join(root, rel), stamp(i++));
	setMtime(root, stamp(i++));
	for (const [rel, m] of Object.entries(dirModes)) fs.chmodSync(path.join(root, rel), m);
}

function walk(root, rel, fn) {
	const abs = rel ? path.join(root, rel) : root;
	for (const name of fs.readdirSync(abs).sort()) {
		const r = rel ? `${rel}/${name}` : name;
		const st = fs.lstatSync(path.join(root, r), { bigint: true });
		fn(r, st);
		if (st.isDirectory()) walk(root, r, fn);
	}
}

function sha256(p) {
	return createHash('sha256').update(fs.readFileSync(p)).digest('hex');
}

// The manifest holds what a host can store. Windows has no permission bits, only the read-only attribute.
function snap(root) {
	const out = [];
	const inodes = new Map();
	walk(root, '', (rel, st) => {
		const e = { path: rel.normalize('NFC') };
		if (st.isSymbolicLink()) {
			e.type = 'symlink';
			e.link = fs.readlinkSync(path.join(root, rel)).split(path.sep).join('/');
		} else if (st.isDirectory()) {
			e.type = 'dir';
		} else if (st.isFile()) {
			e.type = 'file';
			e.size = Number(st.size);
			e.sha256 = sha256(path.join(root, rel));
			if (st.nlink > 1n) {
				const key = `${st.dev}:${st.ino}`;
				if (!inodes.has(key)) inodes.set(key, rel);
				e.hardlink = inodes.get(key);
			}
		} else {
			e.type = 'other';
		}
		if (e.type !== 'symlink') {
			e.mtimeNs = st.mtimeNs.toString();
			e.mode = posix ? `0${(Number(st.mode) & 0o7777).toString(8)}` : Number(st.mode) & 0o200 ? 'rw' : 'ro';
		}
		out.push(e);
	});
	return out;
}

function diff(a, b, opt) {
	const strip = (e) => {
		const c = { ...e };
		if (opt.data) {
			delete c.mtimeNs;
			delete c.mode;
			delete c.hardlink;
		}
		if (opt.noMode) delete c.mode;
		if (opt.mtimeSec && c.mtimeNs) c.mtimeNs = (BigInt(c.mtimeNs) / 1_000_000_000n).toString();
		if (opt.noDirMtime && c.type === 'dir') delete c.mtimeNs;
		return JSON.stringify(c);
	};
	const ma = new Map(a.map((e) => [e.path, strip(e)]));
	const mb = new Map(b.map((e) => [e.path, strip(e)]));
	const problems = [];
	for (const [p, v] of ma) {
		if (!mb.has(p)) problems.push(`missing: ${p}`);
		else if (mb.get(p) !== v) problems.push(`differs: ${p}\n  want ${v}\n  got  ${mb.get(p)}`);
	}
	for (const p of mb.keys()) if (!ma.has(p)) problems.push(`extra: ${p}`);
	return problems;
}

function report(problems, n) {
	if (problems.length) {
		for (const p of problems) console.log(p);
		console.log(`${problems.length} difference(s)`);
		process.exit(1);
	}
	console.log(`same: ${n} entries`);
}

function flags(args) {
	return {
		data: args.includes('--data'),
		mtimeSec: args.includes('--mtime-sec'),
		noDirMtime: args.includes('--no-dir-mtime'),
		noMode: args.includes('--no-mode'),
	};
}

function xattr(op, file, name, value) {
	if (host === 'linux') {
		if (op === 'set') return execFileSync('setfattr', ['-n', `user.${name}`, '-v', value, file]);
		const out = execFileSync('getfattr', ['--only-values', '-n', `user.${name}`, file], { encoding: 'utf8' });
		return console.log(`${name}=${out}`);
	}
	if (host === 'darwin') {
		if (op === 'set') return execFileSync('xattr', ['-w', `user.${name}`, value, file]);
		const out = execFileSync('xattr', ['-p', `user.${name}`, file], { encoding: 'utf8' });
		return console.log(`${name}=${out.trim()}`);
	}
	// NTFS keeps named data in an alternate data stream, the nearest thing it has to an xattr.
	const stream = `${file}:${name}`;
	if (op === 'set') return fs.writeFileSync(stream, value);
	try {
		console.log(`${name}=${fs.readFileSync(stream, 'utf8')}`);
	} catch (e) {
		if (e.code !== 'ENOENT') throw e;
		console.log(`${name} absent`);
	}
}

const [cmd, ...args] = process.argv.slice(2);
switch (cmd) {
	case 'make':
		make(args[0] ?? die('make: DIR is required'));
		console.log(`made ${snap(args[0]).length} entries`);
		break;
	case 'snap':
		console.log(JSON.stringify(snap(args[0] ?? die('snap: DIR is required')), null, '\t'));
		break;
	case 'compare': {
		const [a, b] = args.filter((x) => !x.startsWith('--'));
		if (!a || !b) die('compare: A and B are required');
		const sa = snap(a);
		report(diff(sa, snap(b), flags(args)), sa.length);
		break;
	}
	case 'verify': {
		const [m, d] = args;
		if (!m || !d) die('verify: MANIFEST and DIR are required');
		// A manifest from another host carries its own mode spelling, so only data and the whole-second mtime travel.
		const want = JSON.parse(fs.readFileSync(m, 'utf8'));
		report(diff(want, snap(d), { noMode: true, mtimeSec: true, ...flags(args) }), want.length);
		break;
	}
	case 'hardlinks': {
		const [d, a, b] = args;
		const sa = fs.statSync(path.join(d, a), { bigint: true });
		const sb = fs.statSync(path.join(d, b), { bigint: true });
		const same = sa.ino === sb.ino && sa.dev === sb.dev && sa.ino !== 0n;
		console.log(`${host} hardlinks: ${same ? 'preserved' : 'duplicated'}`);
		break;
	}
	case 'sparse': {
		const st = fs.statSync(args[0], { bigint: true });
		// blocks counts 512-byte units on every posix host. Windows reports none.
		if (!posix) console.log(`${host} sparse: unmeasured`);
		else console.log(`${host} sparse: ${st.blocks * 512n < st.size / 2n ? 'holes kept' : 'filled'}`);
		break;
	}
	case 'xattr':
		xattr(args[0], args[1], args[2], args[3]);
		break;
	case 'touch':
		setMtime(args[0], BigInt(Date.parse(args[1])) * 1_000_000n);
		break;
	case 'host':
		console.log(host);
		break;
	default:
		die(`unknown command '${cmd ?? ''}'`);
}
