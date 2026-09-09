import { createApp } from 'vue'
import { Quasar, Dialog, Notify } from 'quasar'
import langZhCN from 'quasar/lang/zh-CN'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'

import App from './App.vue'
import routes from './router/routes'

import '@quasar/extras/material-icons/material-icons.css'
import 'quasar/src/css/index.sass'
import './styles/app.css'

const app = createApp(App)

app.use(Quasar, {
  plugins: { Dialog, Notify },
  // 全站内置文案（表格分页、日期选择器、对话框等）使用中文
  lang: langZhCN,
  config: {
    notify: { position: 'top', timeout: 2500 }
  }
})

app.use(createPinia())
app.use(createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 })
}))

app.mount('#app')
