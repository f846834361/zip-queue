import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { quasar, transformAssetUrls } from '@quasar/vite-plugin'

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)))

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    vue({
      template: { transformAssetUrls }
    }),
    quasar({
      sassVariables: 'src/quasar-variables.sass'
    })
  ],
  css: {
    preprocessorOptions: {
      sass: {
        // Quasar's index.sass uses `@import 'src/quasar-variables.sass'`
        // resolved relative to the project root, so add it to loadPaths.
        loadPaths: [projectRoot]
      }
    }
  },
  server: {
    host: true,
    port: 9000,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8787',
        changeOrigin: true
      }
    }
  },
  build: {
    rollupOptions: {
      output: {
        // 拆分 vendor：业务代码迭代时框架依赖仍命中浏览器缓存
        manualChunks(id: string) {
          if (!id.includes('node_modules')) return undefined
          if (/[\\/]node_modules[\\/](vue|@vue|vue-router|pinia)[\\/]/.test(id)) return 'vendor-vue'
          if (/[\\/]node_modules[\\/](quasar|@quasar)[\\/]/.test(id)) return 'vendor-quasar'
          if (/[\\/]node_modules[\\/]axios[\\/]/.test(id)) return 'vendor-axios'
          return undefined
        }
      }
    }
  }
})
