export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh items-start justify-center bg-canvas py-4 sm:items-center">
      {children}
    </div>
  );
}
