const { defineConfig } = require('@vue/cli-service')
const fs = require('fs')
const path = require('path')
module.exports = defineConfig({
  transpileDependencies: true,
  lintOnSave: false,
  devServer: {
    host: '127.0.0.1',
    // 此处开启 https,并加载本地证书（否则浏览器左上角会提示不安全）
    https: {
      cert: fs.readFileSync(path.resolve(__dirname, '../../pkg/ssl/local.pem')),
      key: fs.readFileSync(path.resolve(__dirname, '../../pkg/ssl/local-key.pem')),
    },
    port: 8080,
  }
})
