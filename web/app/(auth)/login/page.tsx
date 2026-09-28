'use client';

import { useEffect, useState } from 'react';
import { useAuth } from '@/components/common/auth-context';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { CctraceIcon } from '@/components/icons/cctrace-icon';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

export default function LoginPage() {
  const { login, isLoading, needsSetup } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const handleEmailChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setEmail(e.target.value);
  const handlePasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setPassword(e.target.value);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSubmitting(true);
    try {
      await login(email, password);
      window.location.href = '/';
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setSubmitting(false);
    }
  };

  /**
   * setup이 필요한 경우 setup 화면으로 이동한다.
   */
  useEffect(() => {
    if (needsSetup) window.location.assign('/setup');
  }, [needsSetup]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <CollectingLoader />
      </div>
    );
  }

  if (needsSetup) {
    return null;
  }

  return (
    <section className="w-full max-w-md mx-auto p-6">
      <div className="bg-surface rounded-lg border border-border p-8">
        <div className="flex items-center gap-2 mb-6">
          <CctraceIcon size={24} />
          <h1 className="text-xl font-semibold text-ink">cctrace</h1>
        </div>
        <p className="text-sm text-ink-2 mb-6">Sign in to your dashboard</p>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1">
            <Label htmlFor="email" className="text-xs text-ink-2">
              Email
            </Label>
            <Input
              id="email"
              type="email"
              value={email}
              onChange={handleEmailChange}
              required
              placeholder="you@company.com"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="password" className="text-xs text-ink-2">
              Password
            </Label>
            <Input
              id="password"
              type="password"
              value={password}
              onChange={handlePasswordChange}
              required
            />
          </div>

          {error && <p className="text-sm text-danger">{error}</p>}

          <Button type="submit" disabled={submitting} className="w-full">
            {submitting ? 'Signing in...' : 'Sign In'}
          </Button>
        </form>
      </div>
    </section>
  );
}
