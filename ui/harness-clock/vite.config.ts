import tailwindcss from '@tailwindcss/vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// The harness is a page of its own beside the module's bundle, not a second entry in it: it mounts
// one of the module's screens against a stub transport so that a browser can be driven against the
// real components. It is built alone and never travels in the release.
export default defineConfig({
	plugins: [svelte(), tailwindcss()],
	build: {
		outDir: 'dist',
		emptyOutDir: true,
		target: 'es2022',
	},
});
