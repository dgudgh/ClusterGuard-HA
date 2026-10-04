# 设置：显示偏好

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

状态设置 → 显示偏好 → 当前浏览器持久化/渲染；以 console.html 实际存储键与事件处理为准。它不写集群策略，没有独立后端配置 API。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html) |
| 回归 | [console-engine-pages-audit.cjs](../../../../tools/console-engine-pages-audit.cjs) |
