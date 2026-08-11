#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";

const fix = process.argv.includes("--fix");
const roots = process.argv.slice(2).filter((argument) => argument !== "--fix");
if (roots.length === 0) {
	roots.push("internal");
}

const files = [];
for (const root of roots) {
	walk(path.resolve(root));
}

function walk(entry) {
	const stat = fs.statSync(entry);
	if (stat.isDirectory()) {
		for (const child of fs.readdirSync(entry)) {
			if (child !== ".git" && child !== "node_modules") {
				walk(path.join(entry, child));
			}
		}
		return;
	}
	if (entry.endsWith(".go") || entry.endsWith(".sql")) {
		files.push(entry);
	}
}

const sources = files.map((file) => ({ file, text: fs.readFileSync(file, "utf8") }));
const booleanColumns = new Set();
for (const { text } of sources) {
	for (const match of text.matchAll(/^\s*([a-z_][a-z0-9_]*)\s+BOOLEAN\b/gim)) {
		booleanColumns.add(match[1].toLowerCase());
	}
}

const findings = new Map();
const edits = new Map();

function report(file, text, offset, length, replacement, message) {
	const line = text.slice(0, offset).split("\n").length;
	findings.set(`${file}:${line}:${message}`, { file, line, message });
	if (fix) {
		const fileEdits = edits.get(file) ?? [];
		fileEdits.push({ start: offset, end: offset + length, replacement });
		edits.set(file, fileEdits);
	}
}

function splitTopLevel(valueList) {
	const values = [];
	let start = 0;
	let depth = 0;
	let quote = "";
	for (let index = 0; index < valueList.length; index += 1) {
		const char = valueList[index];
		if (quote) {
			if (char === quote && valueList[index + 1] === quote) {
				index += 1;
			} else if (char === quote) {
				quote = "";
			}
			continue;
		}
		if (char === "'" || char === '"' || char === "`") {
			quote = char;
		} else if (char === "(") {
			depth += 1;
		} else if (char === ")") {
			depth -= 1;
		} else if (char === "," && depth === 0) {
			const raw = valueList.slice(start, index);
			const leading = raw.length - raw.trimStart().length;
			values.push({ value: raw.trim(), start: start + leading });
			start = index + 1;
		}
	}
	const raw = valueList.slice(start);
	const leading = raw.length - raw.trimStart().length;
	values.push({ value: raw.trim(), start: start + leading });
	return values;
}

function readParenthesized(text, start) {
	let depth = 0;
	let quote = "";
	for (let index = start; index < text.length; index += 1) {
		const char = text[index];
		if (quote) {
			if (char === quote && text[index + 1] === quote) {
				index += 1;
			} else if (char === quote) {
				quote = "";
			}
			continue;
		}
		if (char === "'" || char === '"' || char === "`") {
			quote = char;
		} else if (char === "(") {
			depth += 1;
		} else if (char === ")") {
			depth -= 1;
			if (depth === 0) {
				return { body: text.slice(start + 1, index), end: index + 1 };
			}
		}
	}
	return null;
}

function checkProjectedValues(file, text, offset, columns, values) {
	for (let index = 0; index < columns.length && index < values.length; index += 1) {
		const column = columns[index].value.replaceAll(/[^a-z0-9_]/gi, "").toLowerCase();
		if (booleanColumns.has(column) && /^(0|1)$/.test(values[index].value)) {
			const valueOffset = offset + values[index].start;
			const replacement = values[index].value === "1" ? "TRUE" : "FALSE";
			report(file, text, valueOffset, 1, replacement, `${column} uses numeric boolean ${values[index].value}`);
		}
	}
}

for (const { file, text } of sources) {
	for (const match of text.matchAll(/\bBOOLEAN(?:\s+NOT\s+NULL)?\s+DEFAULT\s+([01])\b/gi)) {
		const valueOffset = match.index + match[0].lastIndexOf(match[1]);
		report(file, text, valueOffset, 1, match[1] === "1" ? "TRUE" : "FALSE", `BOOLEAN DEFAULT uses numeric boolean ${match[1]}`);
	}

	for (const match of text.matchAll(/\b([a-z_][a-z0-9_]*)\s*=\s*([01])\b/gi)) {
		if (booleanColumns.has(match[1].toLowerCase())) {
			const lineStart = text.lastIndexOf("\n", match.index) + 1;
			const lineEnd = text.indexOf("\n", match.index);
			const line = text.slice(lineStart, lineEnd === -1 ? text.length : lineEnd);
			if (file.endsWith(".sql") || /\b(?:WHERE|SET|WHEN|AND|OR|ON|CASE)\b/i.test(line)) {
				const valueOffset = match.index + match[0].lastIndexOf(match[2]);
				report(file, text, valueOffset, 1, match[2] === "1" ? "TRUE" : "FALSE", `${match[1]} uses numeric boolean ${match[2]}`);
			}
		}
	}

	const insertPattern = /\bINSERT\s+INTO\s+[a-z_][a-z0-9_.]*\s*\(([^)]*)\)\s*(VALUES|SELECT)\b/gi;
	for (const match of text.matchAll(insertPattern)) {
		const columns = splitTopLevel(match[1]);
		let cursor = match.index + match[0].length;
		if (match[2].toUpperCase() === "VALUES") {
			while (cursor < text.length) {
				while (/\s/.test(text[cursor])) cursor += 1;
				if (text[cursor] !== "(") break;
				const tuple = readParenthesized(text, cursor);
				if (!tuple) break;
				checkProjectedValues(file, text, cursor + 1, columns, splitTopLevel(tuple.body));
				cursor = tuple.end;
				while (/\s/.test(text[cursor])) cursor += 1;
				if (text[cursor] !== ",") break;
				cursor += 1;
			}
		} else {
			const from = text.slice(cursor).search(/\sFROM\b/i);
			if (from !== -1) {
				const projection = text.slice(cursor, cursor + from);
				checkProjectedValues(file, text, cursor, columns, splitTopLevel(projection));
			}
		}
	}
}

if (fix && findings.size > 0) {
	for (const [file, fileEdits] of edits) {
		const uniqueEdits = [...new Map(fileEdits.map((edit) => [`${edit.start}:${edit.end}`, edit])).values()]
			.sort((a, b) => b.start - a.start);
		let updated = fs.readFileSync(file, "utf8");
		for (const edit of uniqueEdits) {
			updated = updated.slice(0, edit.start) + edit.replacement + updated.slice(edit.end);
		}
		fs.writeFileSync(file, updated);
	}
	console.log(`Replaced ${findings.size} numeric SQL boolean literals.`);
	process.exit(0);
}

if (findings.size > 0) {
	for (const finding of [...findings.values()].sort((a, b) =>
		a.file.localeCompare(b.file) || a.line - b.line || a.message.localeCompare(b.message))) {
		console.error(`${path.relative(process.cwd(), finding.file)}:${finding.line}: ${finding.message}`);
	}
	console.error(`Numeric SQL booleans found: ${findings.size}`);
	process.exit(1);
}

console.log(`SQL boolean literal check passed (${files.length} files, ${booleanColumns.size} boolean columns).`);
