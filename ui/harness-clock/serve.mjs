// The harness's server: the built page, on a port, for a browser that has no file:// origin to use.
//
// A module script loaded from file:// is refused by the browser's own origin rules, so the harness is
// built and then served — twenty lines of `node:http`, and no dependency this tree does not already
// have.
import { createReadStream, existsSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(fileURLToPath(new URL('.', import.meta.url)), 'dist');
const port = Number(process.env.PORT ?? 4173);

const types = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'text/javascript; charset=utf-8',
	'.css': 'text/css; charset=utf-8',
	'.json': 'application/json; charset=utf-8',
	'.png': 'image/png',
	'.svg': 'image/svg+xml',
};

createServer((request, response) => {
	const asked = decodeURIComponent((request.url ?? '/').split('?')[0]);
	const wanted = normalize(join(root, asked === '/' ? 'index.html' : asked));

	if (!wanted.startsWith(root) || !existsSync(wanted) || statSync(wanted).isDirectory()) {
		// One page: every hash is the same document.
		response.writeHead(200, { 'content-type': types['.html'] });
		createReadStream(join(root, 'index.html')).pipe(response);

		return;
	}

	response.writeHead(200, { 'content-type': types[extname(wanted)] ?? 'application/octet-stream' });
	createReadStream(wanted).pipe(response);
}).listen(port, '127.0.0.1', () => {
	console.log(`harness on http://127.0.0.1:${port}/`);
});
