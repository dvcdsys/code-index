import type { ReclaimCategory, ReclaimCategoryId } from '@/api/types';
import { formatBytes } from '@/lib/formatBytes';
import { CheckboxRow } from '@/ui/checkbox';

interface Props {
  categories: ReclaimCategory[];
  selected: Set<ReclaimCategoryId>;
  onToggle: (id: ReclaimCategoryId) => void;
  disabled?: boolean;
}

// One row per category. Labels, descriptions and the pre-tick defaults all
// come from the server — the rules about what is safe to reclaim are decided
// in one place, and the UI renders that decision rather than re-deriving it.
export function ReclaimCategoryList({ categories, selected, onToggle, disabled }: Props) {
  return (
    <div className="flex flex-col gap-3.5">
      {categories.map((c) => {
        const empty = c.item_count === 0;
        // Belt and braces: the schema says items is always an array, but a
        // server that serialises an empty slice as null would otherwise take
        // the whole page down rather than render one empty row.
        const items = c.items ?? [];
        return (
          <div key={c.id} className="flex flex-col gap-1.5">
            <CheckboxRow
              id={`reclaim-${c.id}`}
              checked={selected.has(c.id)}
              disabled={disabled || empty}
              onCheckedChange={() => onToggle(c.id)}
              label={
                <span className="flex flex-wrap items-baseline gap-x-2">
                  <span>{c.label}</span>
                  <span className="font-mono text-[12px] text-muted">
                    {empty ? 'nothing to reclaim' : `${formatBytes(c.size_bytes)} · ${c.item_count} items`}
                  </span>
                  {c.estimated_ram_bytes ? (
                    <span className="font-mono text-[12px] text-accent">
                      ≈{formatBytes(c.estimated_ram_bytes)} RAM
                    </span>
                  ) : null}
                </span>
              }
              hint={c.description}
            />
            {items.length > 0 ? (
              <details className="ml-[28px]">
                <summary className="cix-hint cursor-pointer select-none">
                  Show what would be deleted
                </summary>
                <ul className="mt-1.5 flex flex-col gap-1">
                  {items.map((it) => (
                    <li key={it.key} className="flex items-baseline gap-2 font-mono text-[12px]">
                      <span className="min-w-0 truncate text-ink" title={it.key}>
                        {it.label}
                      </span>
                      {it.detail ? <span className="text-dim">{it.detail}</span> : null}
                      <span className="ml-auto flex-none text-muted">{formatBytes(it.size_bytes)}</span>
                    </li>
                  ))}
                  {c.items_truncated ? (
                    <li className="cix-hint">
                      … and {c.item_count - items.length} more. Cleaning covers all{' '}
                      {c.item_count}.
                    </li>
                  ) : null}
                </ul>
              </details>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
