const { defineConfig } = require('@vue/cli-service')
const fs = require('fs')
const path = require('path')
// 本地默认 HTTP；显式启用 HTTPS 时才读取证书。
const httpsEnabled = process.env.VUE_DEV_HTTPS === 'true'
module.exports = defineConfig({
  transpileDependencies: true,
  lintOnSave: false,
  devServer: {
    host: '127.0.0.1',
    https: httpsEnabled ? {
      cert: fs.readFileSync(path.resolve(__dirname, '../../pkg/ssl/local.pem')),
      key: fs.readFileSync(path.resolve(__dirname, '../../pkg/ssl/local-key.pem')),
    } : false,
    port: 8080,
  }
})
