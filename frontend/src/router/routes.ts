import type { RouteRecordRaw } from 'vue-router'
import MainLayout from '../layouts/MainLayout.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    component: MainLayout,
    children: [
      {
        path: '',
        name: 'files',
        component: () => import('../pages/FilesPage.vue'),
        meta: { title: '文件浏览' }
      },
      {
        path: 'tasks',
        name: 'tasks',
        component: () => import('../pages/TasksPage.vue'),
        meta: { title: '任务列表' }
      },
      {
        path: 'tasks/:id',
        name: 'task-detail',
        component: () => import('../pages/TaskDetailPage.vue'),
        props: true,
        meta: { title: '任务详情' }
      },
      {
        path: 'config',
        name: 'config',
        component: () => import('../pages/ConfigPage.vue'),
        meta: { title: '配置' }
      }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/' }
]

export default routes
