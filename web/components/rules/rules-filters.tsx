import { Search } from 'lucide-react';
import { Input } from '@/components/ui/input';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { STATUS_OPTIONS } from './rule-helpers';
import type { StatusFilter } from './rule-helpers';

interface RepositoryOption {
  key: string;
  name: string;
}

interface RulesFiltersProps {
  search: string;
  statusFilter: StatusFilter;
  repositoryFilter: string;
  repositoryOptions: RepositoryOption[];
  onSearchChange: (value: string) => void;
  onStatusChange: (value: StatusFilter) => void;
  onRepositoryChange: (value: string) => void;
}

const ALL_REPOSITORIES_VALUE = '__all__';
const isStatusFilter = (value: string): value is StatusFilter =>
  STATUS_OPTIONS.some((option) => option.value === value);

const RulesFilters = ({
  search,
  statusFilter,
  repositoryFilter,
  repositoryOptions,
  onSearchChange,
  onStatusChange,
  onRepositoryChange,
}: RulesFiltersProps) => {
  const handleSearchChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    onSearchChange(event.target.value);
  };

  const handleStatusChange = (value: string) => {
    if (isStatusFilter(value)) onStatusChange(value);
  };

  const handleRepositoryChange = (value: string) => {
    onRepositoryChange(value === ALL_REPOSITORIES_VALUE ? '' : value);
  };

  return (
    <div className="flex w-full flex-col gap-2 sm:flex-row lg:max-w-[760px]">
      <div className="relative min-w-0 flex-1">
        <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-ink-3" />
        <Input
          value={search}
          onChange={handleSearchChange}
          placeholder="Search rules"
          className="h-[34px] rounded-lg border-border bg-surface pl-9 pr-3 text-[13px] text-ink shadow-none placeholder:text-ink-3 focus-visible:border-brand focus-visible:ring-0"
        />
      </div>
      <Select value={statusFilter} onValueChange={handleStatusChange}>
        <SelectTrigger className="h-[34px] rounded-lg border-border bg-surface px-3 text-[12px] text-ink shadow-none focus-visible:border-brand focus-visible:ring-0">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {STATUS_OPTIONS.map(option => (
            <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={repositoryFilter === '' ? ALL_REPOSITORIES_VALUE : repositoryFilter}
        onValueChange={handleRepositoryChange}
      >
        <SelectTrigger className="h-[34px] rounded-lg border-border bg-surface px-3 text-[12px] text-ink shadow-none focus-visible:border-brand focus-visible:ring-0">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_REPOSITORIES_VALUE}>All repositories</SelectItem>
          {repositoryOptions.map(option => (
            <SelectItem key={option.key} value={option.key}>{option.name}</SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
};

export { RulesFilters };
export type { RepositoryOption };
