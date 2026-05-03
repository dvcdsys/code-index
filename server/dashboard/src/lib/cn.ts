import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

// Standard shadcn helper — clsx for conditional classes + tailwind-merge to
// resolve conflicts (e.g. "px-2 px-4" → "px-4"). Used everywhere instead of
// raw className concatenation.
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
