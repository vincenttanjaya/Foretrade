// Small stroke icon set (24px grid, currentColor). Decorative by default: pair with visible text
// or an aria-label on the parent control.
import type { SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement> & { size?: number }

function Svg({ size = 20, children, ...rest }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.7}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  )
}

export const IconAgent = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3.5l1.8 4.7 4.7 1.8-4.7 1.8L12 16.5l-1.8-4.7L5.5 10l4.7-1.8z" />
    <path d="M18.5 15.5l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" />
  </Svg>
)

export const IconTrade = (p: IconProps) => (
  <Svg {...p}>
    <path d="M7 3.5v3M7 16.5v4M17 4.5v4M17 15.5v3" />
    <rect x="5" y="6.5" width="4" height="10" rx="1" />
    <rect x="15" y="8.5" width="4" height="7" rx="1" />
  </Svg>
)

export const IconForecast = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <circle cx="12" cy="12" r="4.5" />
    <circle cx="12" cy="12" r="0.6" fill="currentColor" />
  </Svg>
)

export const IconStrategy = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9.5 3.5h5M10.5 3.5v5.2L5.6 17.4a2 2 0 0 0 1.8 3.1h9.2a2 2 0 0 0 1.8-3.1l-4.9-8.7V3.5" />
    <path d="M7.8 14h8.4" />
  </Svg>
)

export const IconPortfolio = (p: IconProps) => (
  <Svg {...p}>
    <path d="M11 4a8.5 8.5 0 1 0 9 9h-9z" />
    <path d="M14 2.8A8 8 0 0 1 21.2 10H14z" />
  </Svg>
)

export const IconAccount = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3.5" y="6" width="17" height="13" rx="2.5" />
    <path d="M3.5 10h17M15.5 14.5h2" />
    <path d="M7 6V5a1.5 1.5 0 0 1 1.5-1.5h7A1.5 1.5 0 0 1 17 5v1" />
  </Svg>
)

export const IconSun = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2.5v2M12 19.5v2M4.6 4.6l1.4 1.4M18 18l1.4 1.4M2.5 12h2M19.5 12h2M4.6 19.4L6 18M18 6l1.4-1.4" />
  </Svg>
)

export const IconMoon = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />
  </Svg>
)

export const IconCheck = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 12.5l4.5 4.5L19 7.5" />
  </Svg>
)

export const IconX = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 6l12 12M18 6L6 18" />
  </Svg>
)

export const IconAlert = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.5v5.5M12 16.2v.3" />
  </Svg>
)

export const IconInfo = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 11v5.5M12 7.8v.3" />
  </Svg>
)

export const IconArrowRight = (p: IconProps) => (
  <Svg {...p}>
    <path d="M5 12h14M13 6l6 6-6 6" />
  </Svg>
)

export const IconLogout = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 4.5h3.5a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H14M10 8l-4 4 4 4M6 12h10" />
  </Svg>
)

export const IconEye = (p: IconProps) => (
  <Svg {...p}>
    <path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z" />
    <circle cx="12" cy="12" r="3" />
  </Svg>
)

export const IconEyeOff = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 4l16 16M10.6 6A9.6 9.6 0 0 1 12 5.5c6 0 9.5 6.5 9.5 6.5a17 17 0 0 1-2.8 3.6M6.6 7.3A16.6 16.6 0 0 0 2.5 12S6 18.5 12 18.5a9 9 0 0 0 4.4-1.1" />
    <path d="M9.9 10a3 3 0 0 0 4.1 4.1" />
  </Svg>
)

export const IconPlus = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 5v14M5 12h14" />
  </Svg>
)

export const IconPaperclip = (p: IconProps) => (
  <Svg {...p}>
    <path d="M20 11.5l-7.6 7.6a4.8 4.8 0 0 1-6.8-6.8l8-8a3.2 3.2 0 0 1 4.5 4.5l-8 8a1.6 1.6 0 0 1-2.3-2.3l7.4-7.4" />
  </Svg>
)

export const IconFile = (p: IconProps) => (
  <Svg {...p}>
    <path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8z" />
    <path d="M14 3.5V8h4.5" />
  </Svg>
)

export const IconTrash = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4.5 7h15M9.5 7V5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v2M6.5 7l.8 11.6a1.5 1.5 0 0 0 1.5 1.4h6.4a1.5 1.5 0 0 0 1.5-1.4L17.5 7" />
  </Svg>
)

export const IconArrowUp = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 19V5M6 11l6-6 6 6" />
  </Svg>
)

export const IconArrowDown = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 5v14M6 13l6 6 6-6" />
  </Svg>
)

export const IconStop = (p: IconProps) => (
  <Svg {...p}>
    <rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none" />
  </Svg>
)

export const IconChevron = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 6l6 6-6 6" />
  </Svg>
)

export const IconHistory = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3.5" y="4.5" width="17" height="15" rx="2.5" />
    <path d="M9.5 4.5v15M6 8.5h1M6 11.5h1" />
  </Svg>
)

export const IconNews = (p: IconProps) => (
  <Svg {...p}>
    <path d="M7.5 9.5h-3V18a1.5 1.5 0 0 0 3 0V6A1.5 1.5 0 0 1 9 4.5h9.5A1.5 1.5 0 0 1 20 6v11.5a2 2 0 0 1-2 2H6" />
    <path d="M10.5 8h6.5v3.5h-6.5zM10.5 14.5h6.5M10.5 17h4" />
  </Svg>
)

export const IconExternal = (p: IconProps) => (
  <Svg {...p}>
    <path d="M9 6.5h8.5V15M17.5 6.5L6.5 17.5" />
  </Svg>
)

/** Signals board: a bullish and a bearish arrow. */
export const IconSignal = (p: IconProps) => (
  <Svg {...p}>
    <path d="M8 19.5v-15M4.5 8L8 4.5 11.5 8" />
    <path d="M16 4.5v15M12.5 16l3.5 3.5 3.5-3.5" />
  </Svg>
)

/** Screener: a funnel the market passes through. */
export const IconScreener = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 5h16l-6 7.2V18l-4 2v-7.8z" />
  </Svg>
)

/** Autopilot: a strategy that trades by itself — a loop around a play mark. */
export const IconAutopilot = (p: IconProps) => (
  <Svg {...p}>
    <path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3M19.5 4.5v3.2h-3.2" />
    <path d="M10.5 9.2v5.6l4.4-2.8z" fill="currentColor" stroke="none" />
  </Svg>
)

export const IconBell = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 16.5V11a6 6 0 0 1 12 0v5.5l1.5 1.5h-15z" />
    <path d="M10 20.5a2.2 2.2 0 0 0 4 0" />
  </Svg>
)

/** Automations: a trigger that acts. */
export const IconBolt = (p: IconProps) => (
  <Svg {...p}>
    <path d="M13 3.5 5.5 13.5h6l-1 7 7.5-10h-6z" />
  </Svg>
)
