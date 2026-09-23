import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
    plugins: [react()],
    envPrefix: ['VITE_', 'REACT_APP_'],
    build: { outDir: '../build/default', emptyOutDir: true, cssMinify: false },
    server: { proxy: { '/api': 'http://localhost:3000', '/v1': 'http://localhost:3000' } },
});
