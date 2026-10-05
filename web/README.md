# One Gateway 的前端界面

`web/default` 是唯一的前端主题,由 Vite 构建、Go 后端通过 `go:embed` 嵌入 `web/build/default` 后提供服务。

## 开发

```sh
cd web/default
npm ci --legacy-peer-deps
npm run dev      # 开发服务器,API 代理到 localhost:3000
npm run typecheck
npm run build    # 产物输出到 ../build/default
```

完整的仓库构建顺序见根目录 [README](../README.md)。

## 说明

- 主题名由环境变量 `THEME` 选择,当前仅有 `default`(`common/config/config.go` 中的 `ValidThemes`)。
- 本主题最初由 [JustSong](https://github.com/songquanpeng) 为 One API 开发,One Gateway 在其基础上继续维护。
