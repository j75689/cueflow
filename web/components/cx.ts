export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

export const inputBase =
  "w-full rounded-md border border-line-strong bg-surface px-2.5 py-1.5 text-sm text-fg shadow-card placeholder:text-fg-4 " +
  "transition-[border-color,box-shadow] hover:border-fg-4 focus:border-accent-solid focus:outline-none " +
  "focus:ring-3 focus:ring-accent-solid/20 disabled:bg-subtle disabled:text-fg-3 disabled:shadow-none";
