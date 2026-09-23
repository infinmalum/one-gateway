import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import svgr from 'vite-plugin-svgr';
import { resolve } from 'node:path';

export default defineConfig({
    plugins: [react(), svgr()],
    envPrefix: ['VITE_', 'REACT_APP_'],
    resolve: {
      alias: Object.fromEntries(
        ['App', 'assets', 'config', 'constants', 'contexts', 'hooks', 'layout', 'menu-items', 'routes', 'serviceWorker', 'store', 'themes', 'ui-component', 'utils', 'views'].map((name) =>
          [name, resolve(import.meta.dirname, 'src', name)],
        ),
      ),
    },
    build: { outDir: '../build/berry', emptyOutDir: true, cssMinify: false },
    server: { proxy: { '/api': 'http://localhost:3000', '/v1': 'http://localhost:3000' } },
});
