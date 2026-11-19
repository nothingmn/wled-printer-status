#!/usr/bin/env node
// The check that keeps the mockups' tokens in step with the module's.
//
//     node docs/design/v2/check-theme.mjs
//
// It reads the token names out of `ui/src/tokens.css` and out of `docs/design/v2/theme.css` and
// fails when the two sets differ. Names, not values: the values differ by design (the module maps
// a host token and lets the host's theme resolve it; a static mockup has to carry the resolved
// value), and the whole point of the pair is that neither can quietly grow a token the other has
// never heard of.
//
// Exit code 0 means the two lists are the same. Exit code 1 prints what is missing from where.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const moduleTokens = join(here, '..', '..', '..', 'ui', 'src', 'tokens.css');
const mockTokens = join(here, 'theme.css');

/** Every `--wled-…` custom property a stylesheet declares, in a comment-free copy of it. */
const names = (file) => {
	const text = readFileSync(file, 'utf8')
		// A declaration, not a mention: the token is followed by a colon and a value.
		.replace(/\/\*[\s\S]*?\*\//g, '');

	const found = new Set();
	for (const match of text.matchAll(/(--wled-[a-z0-9-]+)\s*:/g)) found.add(match[1]);

	return found;
};

const module_ = names(moduleTokens);
const mock = names(mockTokens);

const missingFromMock = [...module_].filter((one) => !mock.has(one)).sort();
const missingFromModule = [...mock].filter((one) => !module_.has(one)).sort();

console.log(`module tokens (ui/src/tokens.css): ${module_.size}`);
console.log(`mockup tokens (docs/design/v2/theme.css): ${mock.size}`);
console.log(`missing from the mockups: ${missingFromMock.length}`);
for (const one of missingFromMock) console.log('  ' + one);
console.log(`missing from the module: ${missingFromModule.length}`);
for (const one of missingFromModule) console.log('  ' + one);

const broken = missingFromMock.length + missingFromModule.length;
console.log(`total_errors: ${broken}`);

process.exit(broken === 0 ? 0 : 1);
