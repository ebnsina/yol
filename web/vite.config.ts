import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	// The API allows one origin, so a port that quietly moved would break every request with a
	// cross-origin failure rather than saying the port was taken. Refusing to start says it plainly.
	server: { port: 5173, strictPort: true },
	preview: { port: 4173, strictPort: true },
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// Single page app: the browser talks to the API directly, so there is no
			// server-rendered page and no second place for logic to live.
			adapter: adapter({ fallback: 'index.html' })
		})
	]
});
