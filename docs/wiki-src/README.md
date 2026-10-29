# 使用手册源文件

这个目录是手册的源码，生成结果在 [`../wiki/`](../wiki/index.html)。

- `content/`：18 篇 Markdown。文件头两个注释决定位置：`<!-- slug: xxx -->` 指定输出文件名，`<!-- chapter: 开始|日常使用|管理|参考 -->` 指定侧栏分组。
- `assets/`：`style.css` 与 `app.js`（主题切换、搜索、移动端抽屉）。
- `gen/`：生成器，纯标准库，把 Markdown 子集渲染成静态站，并生成 `search-index.json` 与 `assets/search-index.js`（后者让直接双击打开时搜索也能用）。

## 重新生成

```bash
cd docs/wiki-src
go run ./gen          # 输出到 ./dist
```

把 `dist/` 的内容覆盖到 `../wiki/` 即可（仓库里保留生成结果，方便直接打开，不需要装 Go）。

## Markdown 支持范围

标题、段落、`-` 列表、围栏代码块、管道表格、行内 `` `code` ``、`**粗体**`、`[文字](链接)`、`>` 引用。不支持图片语法——界面截图暂未纳入手册。

## 打开方式

- 直接双击 `../wiki/index.html`。搜索使用随页面加载的 `assets/search-index.js`，所以 `file://` 下也能用。
- 或用任意静态服务器托管 `../wiki/` 目录。
