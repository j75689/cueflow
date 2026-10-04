import Link from "next/link";

export default function NotFound() {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-24 text-center">
      <p className="text-sm font-medium text-zinc-500">404</p>
      <h1 className="text-xl font-semibold">Page not found</h1>
      <Link href="/" className="text-sm text-indigo-600 hover:underline dark:text-indigo-400">
        Back to review
      </Link>
    </div>
  );
}
