// 视觉原型用的示例图片：手写的 SVG 插画，转成 data URI，不依赖外部图片资源。第 4 阶段删除。

function svg(width: number, height: number, body: string): string {
  const markup = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}">${body}</svg>`
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(markup)}`
}

// 火星沙丘日落，4:3 横图
export const MARS_DUNES = {
  width: 1600,
  height: 1200,
  url: svg(
    1600,
    1200,
    `<defs>
      <linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#1A0B08"/><stop offset="0.45" stop-color="#7A1E10"/>
        <stop offset="0.72" stop-color="#E2452C"/><stop offset="1" stop-color="#FFB199"/>
      </linearGradient>
      <radialGradient id="sun" cx="0.5" cy="0.5" r="0.5">
        <stop offset="0" stop-color="#FFF4E8"/><stop offset="0.6" stop-color="#FFD2B8"/><stop offset="1" stop-color="#FFB199" stop-opacity="0"/>
      </radialGradient>
    </defs>
    <rect width="1600" height="1200" fill="url(#sky)"/>
    <circle cx="1080" cy="700" r="210" fill="url(#sun)"/>
    <path d="M0 780 C260 700 420 760 640 720 S1080 640 1300 700 1600 690 1600 690 V1200 H0Z" fill="#9A2A16"/>
    <path d="M0 880 C300 800 520 900 820 840 S1300 780 1600 860 V1200 H0Z" fill="#7A1E10"/>
    <path d="M0 1000 C360 920 700 1040 1000 980 S1420 930 1600 990 V1200 H0Z" fill="#4A130A"/>
    <circle cx="300" cy="180" r="3" fill="#FFE8DC"/><circle cx="520" cy="120" r="2" fill="#FFE8DC"/>
    <circle cx="1380" cy="160" r="2.5" fill="#FFE8DC"/><circle cx="880" cy="90" r="2" fill="#FFE8DC"/>`,
  ),
}

// 登山合影的抽象画，3:4 竖图
export const SUMMIT = {
  width: 1080,
  height: 1440,
  url: svg(
    1080,
    1440,
    `<defs>
      <linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stop-color="#FDEBE6"/><stop offset="0.55" stop-color="#F7C59B"/><stop offset="1" stop-color="#F0A23B"/>
      </linearGradient>
    </defs>
    <rect width="1080" height="1440" fill="url(#sky)"/>
    <circle cx="760" cy="380" r="120" fill="#FFF7F0" opacity="0.9"/>
    <path d="M-40 1000 L260 560 L420 760 L620 420 L900 860 L1120 640 V1440 H-40Z" fill="#B4532A"/>
    <path d="M620 420 L700 540 L650 520 L600 560 L560 500Z" fill="#FFF7F0"/>
    <path d="M-40 1140 L300 820 L520 1000 L800 760 L1120 1060 V1440 H-40Z" fill="#8C2F1B"/>
    <path d="M-40 1300 C240 1180 520 1260 760 1200 S1040 1180 1120 1220 V1440 H-40Z" fill="#3D2B26"/>`,
  ),
}

// 原型界面截图的示意，16:9 横图
export const SCREENSHOT = {
  width: 1920,
  height: 1080,
  url: svg(
    1920,
    1080,
    `<rect width="1920" height="1080" fill="#FAF7F3"/>
    <rect width="96" height="1080" fill="#0F0B0A"/>
    <circle cx="48" cy="60" r="22" fill="#E2452C"/>
    <rect x="96" width="420" height="1080" fill="#FFFFFF"/>
    <rect x="128" y="120" width="356" height="44" rx="10" fill="#F1ECE6"/>
    <rect x="128" y="196" width="356" height="72" rx="14" fill="#FDEBE6"/>
    <rect x="128" y="284" width="356" height="72" rx="14" fill="#FFFFFF" stroke="#E8E1DA"/>
    <rect x="128" y="372" width="356" height="72" rx="14" fill="#FFFFFF" stroke="#E8E1DA"/>
    <rect x="1080" y="40" width="200" height="40" rx="20" fill="#17110F"/>
    <rect x="600" y="200" width="520" height="64" rx="22" fill="#F1ECE6"/>
    <rect x="600" y="280" width="380" height="64" rx="22" fill="#F1ECE6"/>
    <rect x="1240" y="380" width="560" height="64" rx="22" fill="#C2361F"/>
    <rect x="1400" y="460" width="400" height="64" rx="22" fill="#C2361F"/>
    <rect x="600" y="560" width="460" height="64" rx="22" fill="#F1ECE6"/>
    <rect x="580" y="940" width="1280" height="88" rx="18" fill="#FFFFFF" stroke="#E8E1DA"/>
    <circle cx="1810" cy="984" r="26" fill="#C2361F"/>`,
  ),
}
