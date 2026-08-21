export type RosterFlags = {
  pinned?: boolean;
  hidden?: boolean;
  unread?: boolean;
};

export function visibleSortedBots<
  B extends { id: string },
  A extends RosterFlags & { bot: { id: string } },
>(bots: B[], agents: A[] | undefined, showHidden: boolean): B[] {
  const byID = new Map((agents ?? []).map((agent) => [agent.bot.id, agent]));
  const rows = bots.map((bot, index) => ({
    bot,
    agent: byID.get(bot.id),
    index,
  }));
  const visible = showHidden
    ? rows
    : rows.filter((row) => !row.agent?.hidden);
  visible.sort((left, right) => {
    const pin =
      Number(Boolean(right.agent?.pinned)) - Number(Boolean(left.agent?.pinned));
    return pin !== 0 ? pin : left.index - right.index;
  });
  return visible.map((row) => row.bot);
}

export function hiddenAgentCount<A extends RosterFlags>(agents: A[] | undefined) {
  return (agents ?? []).filter((agent) => agent.hidden).length;
}
