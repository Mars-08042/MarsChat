// 布局与页面的动态导入（设计 2.3 按路由拆包）。路由表和预取共用这里的函数，
// 同一个模块重复导入时由浏览器缓存，不会重复下载。

export const layouts = {
  auth: () => import("@/features/auth/AuthLayout").then((m) => m.AuthLayout),
  shell: () => import("./AppShell").then((m) => m.AppShell),
}

export const pages = {
  login: () => import("@/features/auth/LoginPage").then((m) => m.LoginPage),
  register: () => import("@/features/auth/RegisterPage").then((m) => m.RegisterPage),
  chat: () => import("@/features/chat/ChatPage").then((m) => m.ChatPage),
  contacts: () => import("@/features/contacts/ContactsPage").then((m) => m.ContactsPage),
  groups: () => import("@/features/groups/GroupsPage").then((m) => m.GroupsPage),
  settings: () => import("@/features/settings/SettingsPage").then((m) => m.SettingsPage),
  admin: () => import("@/features/admin/AdminPage").then((m) => m.AdminPage),
}

// 登录后的页面：主框架挂载后在后台预取，第一次切换页面时不用等下载
export function preloadAppPages() {
  for (const load of [pages.chat, pages.contacts, pages.groups, pages.settings, pages.admin]) {
    load().catch(() => {
      // 预取失败不处理：真正切换到这个页面时会再下载一次，失败由路由的错误处理接管
    })
  }
}
