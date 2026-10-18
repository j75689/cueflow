import Link from "next/link";

export default function NotFound() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-24 text-center">
      <p className="text-sm font-medium text-fg-3">404</p>
      <h1 className="text-xl font-semibold">Page not found</h1>
      <Link href="/" className="text-sm text-accent hover:underline">
        Back to review
      </Link>
    </div>
  );
}
