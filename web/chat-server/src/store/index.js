import { createStore } from 'vuex'

export default createStore({
  state: {
    // 部署时通过 Vue CLI 环境变量覆盖本地默认地址。
    backendUrl: process.env.VUE_APP_BACKEND_URL || 'https://127.0.0.1:8000',
    wsUrl: process.env.VUE_APP_WS_URL || 'wss://127.0.0.1:8000',
    // 信令服务器地址
    // signalUrl: 'wss://127.0.0.1:8001',
    userInfo: (sessionStorage.getItem('userInfo') && JSON.parse(sessionStorage.getItem('userInfo'))) || {},
    socket: null,
  },
  getters: {
  },
  mutations: {
    setUserInfo(state, userInfo) {
      state.userInfo = userInfo;
      sessionStorage.setItem('userInfo', JSON.stringify(userInfo));
    },
    cleanUserInfo(state) {
      state.userInfo = {};
      sessionStorage.removeItem('userInfo');
    }
  },
  actions: {
  },
  modules: {
  }
})
