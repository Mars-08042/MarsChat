import { useId } from "react"

// MarsChat 标志：火星形状的对话气泡，左下角是气泡尾巴，表面有三个陨石坑
export function MarsLogo({ className }: { className?: string }) {
  const id = useId()
  const gradient = `${id}-planet`

  return (
    <svg viewBox="0 0 32 32" fill="none" aria-hidden="true" className={className}>
      <defs>
        <radialGradient id={gradient} cx="38%" cy="30%" r="78%">
          <stop offset="0%" stopColor="#FFB199" />
          <stop offset="42%" stopColor="#E2452C" />
          <stop offset="100%" stopColor="#7A1E10" />
        </radialGradient>
      </defs>
      <path d="M17 3a12 12 0 1 1-6.9 21.8L4 27l2.2-6.1A12 12 0 0 1 17 3z" fill={`url(#${gradient})`} />
      <circle cx="21.5" cy="10.5" r="2.2" fill="#7A1E10" fillOpacity="0.35" />
      <circle cx="13" cy="17.5" r="1.6" fill="#7A1E10" fillOpacity="0.3" />
      <circle cx="21" cy="19.5" r="1" fill="#7A1E10" fillOpacity="0.3" />
    </svg>
  )
}
