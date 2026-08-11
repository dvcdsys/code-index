import { Card, CardBody, CardHead } from '@/ui/card';
import { RadioCard, RadioGroup } from '@/ui/radio-group';
import { useTheme } from '@/app/ThemeProvider';
import type { ThemeMode } from '@/lib/theme';

const OPTIONS: ReadonlyArray<{ value: ThemeMode; label: string; hint: string }> = [
  { value: 'light', label: 'Cream', hint: 'always the light theme' },
  { value: 'dark', label: 'Ink', hint: 'always the dark theme' },
  { value: 'system', label: 'System', hint: 'follow prefers-color-scheme' },
];

export function ThemeSection() {
  const { mode, resolved, setMode } = useTheme();

  return (
    <Card>
      <CardHead
        title="Theme"
        aside={<span className="font-mono text-[11px] font-normal text-muted">{resolved}</span>}
      />
      <CardBody>
        <p className="mb-3.5 mt-0 text-[13.5px] text-dim">
          Stored locally — it applies to this browser only.
        </p>
        <RadioGroup value={mode} onValueChange={(v) => setMode(v as ThemeMode)}>
          {OPTIONS.map((o) => (
            <RadioCard
              key={o.value}
              id={`theme-${o.value}`}
              value={o.value}
              selected={mode === o.value}
              title={o.label}
              hint={o.hint}
            />
          ))}
        </RadioGroup>
      </CardBody>
    </Card>
  );
}
