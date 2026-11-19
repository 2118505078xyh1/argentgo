//! Reduced-motion helpers. Kept DOM-agnostic so the policy can be unit-tested;
//! the component only calls prefersReducedMotion() when a window exists.

export type ScrollBehavior = "auto" | "smooth";

export function prefersReducedMotion(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

export function scrollBehavior(reduced: boolean): ScrollBehavior {
  return reduced ? "auto" : "smooth";
}
