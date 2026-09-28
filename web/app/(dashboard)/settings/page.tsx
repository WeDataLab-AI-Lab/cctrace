'use client';

import { useState } from 'react';
import { KeyRound, Shield } from 'lucide-react';
import { changePassword, refreshToken } from '@/lib/api';
import { useAuth } from '@/components/common/auth-context';
import { AppearanceControls } from '@/components/common/appearance-controls';
import { APITokenManagement } from '@/components/settings/api-token-management';
import { BillingSelfExclusion } from '@/components/settings/billing-self-exclusion';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

export default function SettingsPage() {
  const { user } = useAuth();
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [error, setError] = useState('');
  const [success, setSuccess] = useState(false);
  const [loading, setLoading] = useState(false);

  const handleCurrentPasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setCurrentPassword(e.target.value);
  const handleNewPasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setNewPassword(e.target.value);
  const handleConfirmPasswordChange = (e: React.ChangeEvent<HTMLInputElement>) =>
    setConfirmPassword(e.target.value);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess(false);

    if (newPassword !== confirmPassword) {
      setError('New passwords do not match');
      return;
    }
    if (newPassword.length < 8) {
      setError('Password must be at least 8 characters');
      return;
    }

    setLoading(true);
    try {
      await changePassword(currentPassword, newPassword);
      await refreshToken(); // JWT 갱신 (must_change_password = false)
      setSuccess(true);
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      // Reload to update auth state across the app
      window.location.href = '/';
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to change password');
    } finally {
      setLoading(false);
    }
  };

  return (
    <section className="mx-auto max-w-3xl pt-4">
      {user?.must_change_password && (
        <div className="mb-5 flex items-start gap-3 p-4 bg-warning-soft border border-warning/40 rounded-lg">
          <Shield size={16} className="text-warning-strong mt-0.5 shrink-0" />
          <div>
            <p className="text-[13px] text-warning-strong font-medium">
              You are using a temporary password.
            </p>
            <p className="text-[12px] text-warning-strong/70 mt-0.5">
              Please change it to continue using the dashboard.
            </p>
          </div>
        </div>
      )}

      <AppearanceControls />

      <APITokenManagement />

      <BillingSelfExclusion />

      <div className="bg-surface rounded-lg border border-border p-8">
        <div className="flex items-center gap-2.5 mb-1">
          <KeyRound size={18} className="text-brand" />
          <h2 className="text-[16px] font-semibold text-ink">Change Password</h2>
        </div>
        <p className="text-[12px] text-ink-3 mb-6 ml-[30px]">Update your account password</p>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1">
            <Label htmlFor="current-password" className="text-xs text-ink-2">
              Current Password
            </Label>
            <Input
              id="current-password"
              type="password"
              value={currentPassword}
              onChange={handleCurrentPasswordChange}
              required
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="new-password" className="text-xs text-ink-2">
              New Password
            </Label>
            <Input
              id="new-password"
              type="password"
              value={newPassword}
              onChange={handleNewPasswordChange}
              required
              minLength={8}
              placeholder="8 characters minimum"
            />
          </div>

          <div className="space-y-1">
            <Label htmlFor="confirm-password" className="text-xs text-ink-2">
              Confirm New Password
            </Label>
            <Input
              id="confirm-password"
              type="password"
              value={confirmPassword}
              onChange={handleConfirmPasswordChange}
              required
              minLength={8}
            />
          </div>

          {error && <p className="text-sm text-danger">{error}</p>}

          {success && (
            <div className="flex items-center gap-2 p-3 bg-success-soft border border-success/30 rounded-md">
              <p className="text-[13px] text-success-strong font-medium">
                Password changed successfully.
              </p>
            </div>
          )}

          <Button type="submit" disabled={loading} className="w-full">
            {loading ? 'Changing...' : 'Change Password'}
          </Button>
        </form>
      </div>
    </section>
  );
}
