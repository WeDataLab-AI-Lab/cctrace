import type { PluginUsageSummary, SkillUsageSummary } from '@/lib/types';
import { projectIdentityKey, projectIdentityLabel } from '@/lib/project-identity';
import { buildUserNames, normalizeName, totalCalls, userKey, userLabel } from './usage-format';
import type { ByUserGroup, CombinedUsage, UsageGroup, UserUsage } from './usage-types';

const combineUsage = (pluginRows: PluginUsageSummary[], skillRows: SkillUsageSummary[]) => {
  const map = new Map<string, CombinedUsage>();

  const ensure = (rawName: string) => {
    const name = normalizeName(rawName);
    const existing = map.get(name);
    if (existing) return existing;
    const row: CombinedUsage = {
      name,
      pluginCalls: 0,
      skillCalls: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      users: new Set<string>(),
    };
    map.set(name, row);
    return row;
  };

  const names = buildUserNames([...pluginRows, ...skillRows]);

  for (const plugin of pluginRows) {
    const id = userKey(plugin.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(plugin.command_name);
    if ((plugin.agent || 'claude') === 'claude') {
      row.pluginCalls += plugin.invocation_count;
    }
    row.totalTokens += plugin.total_tokens;
    row.inputTokens += plugin.input_tokens;
    row.outputTokens += plugin.output_tokens;
    row.users.add(user);
  }

  for (const skill of skillRows) {
    const id = userKey(skill.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(skill.skill_name);
    row.skillCalls += skill.total_count;
    row.users.add(user);
  }

  return Array.from(map.values()).sort((a, b) => {
    const aTotal = totalCalls(a);
    const bTotal = totalCalls(b);
    if (bTotal !== aTotal) return bTotal - aTotal;
    return a.name.localeCompare(b.name);
  });
};

const combineUsageBySkillUser = (
  pluginRows: PluginUsageSummary[],
  skillRows: SkillUsageSummary[]
) => {
  const bySkill = new Map<string, Map<string, UserUsage>>();

  const ensure = (rawName: string, user: string) => {
    const name = normalizeName(rawName);
    let userMap = bySkill.get(name);
    if (!userMap) {
      userMap = new Map<string, UserUsage>();
      bySkill.set(name, userMap);
    }
    const existing = userMap.get(user);
    if (existing) return existing;
    const row: UserUsage = {
      user,
      pluginCalls: 0,
      skillCalls: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
    };
    userMap.set(user, row);
    return row;
  };

  const names = buildUserNames([...pluginRows, ...skillRows]);

  for (const plugin of pluginRows) {
    const id = userKey(plugin.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(plugin.command_name, user);
    if ((plugin.agent || 'claude') === 'claude') {
      row.pluginCalls += plugin.invocation_count;
    }
    row.totalTokens += plugin.total_tokens;
    row.inputTokens += plugin.input_tokens;
    row.outputTokens += plugin.output_tokens;
  }

  for (const skill of skillRows) {
    const id = userKey(skill.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(skill.skill_name, user);
    row.skillCalls += skill.total_count;
  }

  const result = new Map<string, UserUsage[]>();
  for (const [skill, userMap] of bySkill.entries()) {
    result.set(
      skill,
      Array.from(userMap.values()).sort((a, b) => {
        const aTotal = a.pluginCalls + a.skillCalls;
        const bTotal = b.pluginCalls + b.skillCalls;
        if (bTotal !== aTotal) return bTotal - aTotal;
        if (b.totalTokens !== a.totalTokens) return b.totalTokens - a.totalTokens;
        return a.user.localeCompare(b.user);
      })
    );
  }
  return result;
};

const combineUsageByUser = (
  pluginRows: PluginUsageSummary[],
  skillRows: SkillUsageSummary[]
): ByUserGroup[] => {
  const byUser = new Map<string, Map<string, CombinedUsage>>();

  const ensure = (user: string, rawName: string) => {
    const name = normalizeName(rawName);
    let itemMap = byUser.get(user);
    if (!itemMap) {
      itemMap = new Map<string, CombinedUsage>();
      byUser.set(user, itemMap);
    }
    const existing = itemMap.get(name);
    if (existing) return existing;
    const row: CombinedUsage = {
      name,
      pluginCalls: 0,
      skillCalls: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      users: new Set<string>([user]),
    };
    itemMap.set(name, row);
    return row;
  };

  const names = buildUserNames([...pluginRows, ...skillRows]);

  for (const plugin of pluginRows) {
    const id = userKey(plugin.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(user, plugin.command_name);
    if ((plugin.agent || 'claude') === 'claude') {
      row.pluginCalls += plugin.invocation_count;
    }
    row.totalTokens += plugin.total_tokens;
    row.inputTokens += plugin.input_tokens;
    row.outputTokens += plugin.output_tokens;
  }

  for (const skill of skillRows) {
    const id = userKey(skill.user_id);
    if (id === '') continue;
    const user = userLabel(id, names);
    const row = ensure(user, skill.skill_name);
    row.skillCalls += skill.total_count;
  }

  return Array.from(byUser.entries())
    .map(([user, itemMap]) => {
      const items = Array.from(itemMap.values()).sort((a, b) => {
        const aTotal = totalCalls(a);
        const bTotal = totalCalls(b);
        if (bTotal !== aTotal) return bTotal - aTotal;
        return a.name.localeCompare(b.name);
      });
      return {
        user,
        items,
        pluginCalls: items.reduce((s, r) => s + r.pluginCalls, 0),
        skillCalls: items.reduce((s, r) => s + r.skillCalls, 0),
        totalTokens: items.reduce((s, r) => s + r.totalTokens, 0),
        inputTokens: items.reduce((s, r) => s + r.inputTokens, 0),
        outputTokens: items.reduce((s, r) => s + r.outputTokens, 0),
      };
    })
    .sort((a, b) => {
      const aTotal = a.pluginCalls + a.skillCalls;
      const bTotal = b.pluginCalls + b.skillCalls;
      if (bTotal !== aTotal) return bTotal - aTotal;
      return a.user.localeCompare(b.user);
    });
};

const groupUsageByDimension = (
  pluginRows: PluginUsageSummary[],
  skillRows: SkillUsageSummary[],
  getPluginGroup: (row: PluginUsageSummary) => { key: string; label: string },
  getSkillGroup: (row: SkillUsageSummary) => { key: string; label: string }
): UsageGroup[] => {
  const byGroup = new Map<string, { label: string; items: Map<string, CombinedUsage> }>();

  const ensure = (group: { key: string; label: string }, rawName: string) => {
    let groupRow = byGroup.get(group.key);
    if (!groupRow) {
      groupRow = { label: group.label, items: new Map<string, CombinedUsage>() };
      byGroup.set(group.key, groupRow);
    }
    const name = normalizeName(rawName);
    const existing = groupRow.items.get(name);
    if (existing) return existing;
    const row: CombinedUsage = {
      name,
      pluginCalls: 0,
      skillCalls: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      users: new Set<string>(),
    };
    groupRow.items.set(name, row);
    return row;
  };

  const names = buildUserNames([...pluginRows, ...skillRows]);

  for (const plugin of pluginRows) {
    const id = userKey(plugin.user_id);
    const row = ensure(getPluginGroup(plugin), plugin.command_name);
    if ((plugin.agent || 'claude') === 'claude') {
      row.pluginCalls += plugin.invocation_count;
    }
    row.totalTokens += plugin.total_tokens;
    row.inputTokens += plugin.input_tokens;
    row.outputTokens += plugin.output_tokens;
    if (id !== '') row.users.add(userLabel(id, names));
  }

  for (const skill of skillRows) {
    const id = userKey(skill.user_id);
    const row = ensure(getSkillGroup(skill), skill.skill_name);
    row.skillCalls += skill.total_count;
    if (id !== '') row.users.add(userLabel(id, names));
  }

  return Array.from(byGroup.entries())
    .map(([key, group]) => {
      const items = Array.from(group.items.values()).sort((a, b) => {
        const aTotal = totalCalls(a);
        const bTotal = totalCalls(b);
        if (bTotal !== aTotal) return bTotal - aTotal;
        return a.name.localeCompare(b.name);
      });
      return {
        key,
        label: group.label,
        items,
        pluginCalls: items.reduce((s, r) => s + r.pluginCalls, 0),
        skillCalls: items.reduce((s, r) => s + r.skillCalls, 0),
        totalTokens: items.reduce((s, r) => s + r.totalTokens, 0),
        inputTokens: items.reduce((s, r) => s + r.inputTokens, 0),
        outputTokens: items.reduce((s, r) => s + r.outputTokens, 0),
      };
    })
    .sort((a, b) => {
      const aTotal = a.pluginCalls + a.skillCalls;
      const bTotal = b.pluginCalls + b.skillCalls;
      if (bTotal !== aTotal) return bTotal - aTotal;
      return a.label.localeCompare(b.label);
    });
};

const projectGroup = (
  row: Pick<
    PluginUsageSummary,
    'project_hash' | 'project_name' | 'repository_id' | 'repository_name' | 'repo_subpath'
  >
) => ({ key: projectIdentityKey(row), label: projectIdentityLabel(row) });

const hasGitProject = (row: { has_git?: boolean; repository_id?: string }) => {
  if (row.has_git) return true;
  const repositoryID = row.repository_id?.trim() ?? '';
  return repositoryID !== '' && !repositoryID.startsWith('local:');
};

export {
  combineUsage,
  combineUsageBySkillUser,
  combineUsageByUser,
  groupUsageByDimension,
  projectGroup,
  hasGitProject,
};
