# 第三方源码声明

本文件登记**复制进仓库**的第三方源码，以及它们的许可证。通过 npm 安装的依赖不在此列，它们的许可证随包分发。规则见[前端设计 3.3 节](../docs/design/frontend.md#33-组件来源与许可证)。

| 文件 | 来源 | 许可证 | 备注 |
|---|---|---|---|
| `src/components/ui/*` | [shadcn/ui](https://github.com/shadcn-ui/ui)（CLI 生成） | MIT | 按设计规范修改过样式 |
| `src/components/effects/SoftAurora.tsx` | [React Bits Soft Aurora](https://reactbits.dev/backgrounds/soft-aurora)（TS + Tailwind 版本） | MIT + Commons Clause（全文见下） | 2026-11-21 获取，除文件头外未修改；登录页背景候选 |
| `src/components/effects/Grainient.tsx` | [React Bits Grainient](https://reactbits.dev/backgrounds/grainient)（TS + Tailwind 版本） | 同上 | 同上 |
| `src/components/effects/Silk.tsx` | [React Bits Silk](https://reactbits.dev/backgrounds/silk)（TS + Tailwind 版本） | 同上 | 同上 |
| `src/components/effects/BlurText.tsx` | [React Bits Blur Text](https://reactbits.dev/text-animations/blur-text)（TS + Tailwind 版本） | 同上 | 2026-11-21 获取，除文件头外未修改；登录页标题 |
| `src/components/effects/RotatingText.tsx` | [React Bits Rotating Text](https://reactbits.dev/text-animations/rotating-text)（TS + Tailwind 版本） | 同上 | 2026-11-21 获取，除文件头外未修改；登录页卖点轮播 |

登记要求：

- **React Bits**（MIT + Commons Clause）：文件头保留原版权声明和许可证说明，并在上表登记组件名与来源链接。可以作为应用的一部分使用和发布，不能单独再分发组件本身。
- **uiverse.io**（MIT）：代码注释写明作者和原始链接，并在上表登记。
- **Aceternity UI 和付费组件**：只参考设计，不提交源码，不在此登记。
- **平台表情图片**（微信、QQ、B站等）：版权属于各平台，不提交进仓库，不在此登记。图片由 `make web-emoji` 在本地生成到 `public/emoji-packs/`（已被 git 忽略），来源仓库、提交和协议写在生成目录的 README 里（[设计第 10 节第 19 条](../docs/design/frontend.md#10-实施中的调整)）。

## React Bits 许可证全文

以下为 [DavidHDev/react-bits](https://github.com/DavidHDev/react-bits) 仓库 `LICENSE.md` 的原文（2026-11-21 获取），按其要求随复制的源码一起提供。

```text
MIT + Commons Clause License Condition v1.0

Copyright (c) 2026 David Haz

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, and distribute the Software **as part of an application, website, or product**, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

## Commons Clause Restriction

You may use this Software, including for any commercial purpose, **so long as you do not sell, sublicense, or redistribute the components themselves-whether alone, in a bundle, or as a ported version.**

The same restriction covers the React Bits tools (Background Studio, Texture Lab, Shape Magic, Center Stage, Sound Lab and any tools added later) and the sounds in the sound library. **You may not sell, sublicense, or redistribute the tools or the sounds themselves, or offer the tools or a substantially similar copy of them as a hosted service-whether alone, in a bundle, or as a ported version.**

What you make with the tools is yours to use. You can use exported code, images, videos, and audio files in any personal or commercial application, website, or product, including paid ones. **You may not sell or redistribute the exports on their own, either individually or as the product itself, such as a sound, texture, background, or template pack.**

## No Warranty

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
