import { cn } from "@/lib/utils";

/**
 * LogoMark: orange rounded tile with a white "V" stroke and a small status
 * dot. Uses the theme's primary colour so it follows light/dark tokens;
 * public/favicon.svg is the same drawing with hard-coded colours.
 */
export function LogoMark({ className, size = 32, shadow }: { className?: string; size?: number; shadow?: boolean }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      role="img"
      aria-label="VpsCT"
      // drop-shadow follows the tile's rounded shape (box-shadow would not)
      className={cn("shrink-0 overflow-visible", shadow && "[filter:drop-shadow(0_8px_14px_hsl(var(--primary)/0.4))]", className)}
    >
      <rect width="64" height="64" rx="18" fill="hsl(var(--primary))" />
      <path d="M18 21 L32 47 L46 21" fill="none" stroke="#fff" strokeWidth="8" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="50" cy="14" r="4.5" fill="#fff" fillOpacity="0.85" />
    </svg>
  );
}

/** Wordmark; the default product name gets a two-tone "Vps" + "CT". */
export function Wordmark({ name = "VpsCT", className }: { name?: string; className?: string }) {
  if (name === "VpsCT") {
    return (
      <span className={cn("font-bold tracking-tight", className)}>
        Vps<span className="text-primary">CT</span>
      </span>
    );
  }
  return <span className={cn("font-bold tracking-tight", className)}>{name}</span>;
}
