'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { setup } from '@/lib/api';
import { withBasePath } from '@/lib/base-path';
import { useAuth } from '@/components/common/auth-context';
import { CollectingLoader } from '@/components/common/collecting-loader';
import { CctraceIcon } from '@/components/icons/cctrace-icon';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

export default function SetupPage() {
  const router = useRouter();
  const { needsSetup, isLoading } = useAuth();
  const [setupToken, setSetupToken] = useState('');
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const handleSetupTokenChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setSetupToken(e.target.value);
  const handleEmailChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setEmail(e.target.value);
  const handleNameChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setName(e.target.value);
  const handlePasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setPassword(e.target.value);
  const handleConfirmPasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setConfirmPassword(e.target.value);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (password !== confirmPassword) {
      setError('Passwords do not match');
      return;
    }
    if (password.length < 8) {
      setError('Password must be at least 8 characters');
      return;
    }

    setSubmitting(true);
    try {
      await setup(email, name, password, setupToken);
      window.location.href = withBasePath('/');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Setup failed');
    } finally {
      setSubmitting(false);
    }
  };

  /** 셋업이 이미 완료된 상태면 로그인 페이지로 보낸다. */
  useEffect(() => {
    if (!isLoading && !needsSetup) {
      router.replace('/login');
    }
  }, [isLoading, needsSetup, router]);

  if (isLoading || !needsSetup) {
    return (
      <div className="flex min-h-dvh items-center justify-center">
        <CollectingLoader />
      </div>
    );
  }

  return (
    <section className="mx-auto w-full max-w-md p-3 sm:p-6">
      <div className="rounded-lg border border-border bg-surface p-4 sm:p-8">
        <div className="flex items-center gap-2 mb-6">
          <CctraceIcon size={24} />
          <h1 className="text-xl font-semibold text-ink">cctrace Setup</h1>
        </div>
        <p className="text-sm text-ink-2 mb-6">
          Create the first administrator account to get started.
        </p>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1">
            <Label htmlFor="setup-token" className="text-xs text-ink-2">
              Setup token
            </Label>
            <Input
              id="setup-token"
              type="password"
              value={setupToken}
              onChange={handleSetupTokenChange}
              autoComplete="off"
              required
              className="min-h-11 text-base sm:text-sm"
            />
            <p className="text-xs text-ink-2">
              Use the token from the server logs or CCTRACE_SETUP_TOKEN.
              It expires after the first administrator is created.
            </p>
          </div>
          <div className="space-y-1">
            <Label htmlFor="email" className="text-xs text-ink-2">
              Email
            </Label>
            <Input
              id="email"
              type="email"
              autoComplete="email"
              value={email}
              onChange={handleEmailChange}
              required
              className="min-h-11 text-base sm:text-sm"
              placeholder="admin@company.com"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="name" className="text-xs text-ink-2">
              Name
            </Label>
            <Input
              id="name"
              type="text"
              autoComplete="name"
              value={name}
              onChange={handleNameChange}
              required
              className="min-h-11 text-base sm:text-sm"
              placeholder="Admin"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="password" className="text-xs text-ink-2">
              Password
            </Label>
            <Input
              id="password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={handlePasswordChange}
              required
              className="min-h-11 text-base sm:text-sm"
              minLength={8}
              placeholder="Min 8 characters"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="confirm-password" className="text-xs text-ink-2">
              Confirm Password
            </Label>
            <Input
              id="confirm-password"
              type="password"
              autoComplete="new-password"
              value={confirmPassword}
              onChange={handleConfirmPasswordChange}
              required
              className="min-h-11 text-base sm:text-sm"
            />
          </div>

          {error && <p className="text-sm text-danger">{error}</p>}

          <Button type="submit" disabled={submitting} className="min-h-11 w-full">
            {submitting ? 'Creating...' : 'Create Admin Account'}
          </Button>
        </form>
      </div>
    </section>
  );
}
