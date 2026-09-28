'use client';

import { KeyRound, MoreVertical, Pencil, Power, ShieldOff, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import type { DashboardUserInfo } from '@/lib/types';

interface UserActionsMenuProps {
  user: DashboardUserInfo;
  onEdit: () => void;
  onToggleActive: () => void;
  onResetPassword: () => void;
  onRevokeToken: () => void;
  onClearData: () => void;
}

const ITEM_CLASS = 'gap-2 px-3 py-2 text-[13px] text-ink focus:bg-canvas';
const DANGER_ITEM_CLASS = 'gap-2 px-3 py-2 text-[13px] text-danger focus:bg-danger-soft focus:text-danger';

const UserActionsMenu = ({ user, onEdit, onToggleActive, onResetPassword, onRevokeToken, onClearData }: UserActionsMenuProps) => {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="size-auto p-1.5 rounded-md text-ink-3 hover:bg-surface-sunk hover:text-ink"
        >
          <MoreVertical size={15} />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        sideOffset={4}
        className="w-44 border-border bg-surface py-1 shadow-[var(--sh-pop)]"
      >
        <DropdownMenuItem onSelect={onEdit} className={ITEM_CLASS}>
          <Pencil size={13} className="text-ink-2" /> Edit
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={onToggleActive} className={ITEM_CLASS}>
          <Power size={13} className="text-ink-2" />
          {user.is_active ? 'Deactivate' : 'Activate'}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={onResetPassword} className={ITEM_CLASS}>
          <KeyRound size={13} className="text-ink-2" /> Reset Password
        </DropdownMenuItem>
        {user.has_api_token && (
          <DropdownMenuItem onSelect={onRevokeToken} className={DANGER_ITEM_CLASS}>
            <ShieldOff size={13} className="text-danger" /> Revoke API Token
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator className="bg-surface-sunk" />
        <DropdownMenuItem onSelect={onClearData} className={DANGER_ITEM_CLASS}>
          <Trash2 size={13} className="text-danger" /> Clear Collected Data
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
};

export { UserActionsMenu };
export type { UserActionsMenuProps };
