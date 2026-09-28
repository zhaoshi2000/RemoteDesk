import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({
  base: '/admin/',
  plugins: [vue()],
  build: { outDir: '../../internal/server/static', emptyOutDir: true, sourcemap: false,
    rollupOptions: { output: { manualChunks: { vue: ['vue'], element: ['element-plus'] } } } },
})
