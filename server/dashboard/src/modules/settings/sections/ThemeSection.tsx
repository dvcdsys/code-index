import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Label } from '@/ui/label';
import { RadioGroup, RadioGroupItem } from '@/ui/radio-group';
import { useTheme } from '@/app/ThemeProvider';
import type { ThemeMode } from '@/lib/theme';

const OPTIONS: ReadonlyArray<{ value: ThemeMode; label: string; hint: string }> = [
  { value: 'light', label: 'Light', hint: 'Always use the light theme.' },
  { value: 'dark', label: 'Dark', hint: 'Always use the dark theme.' },
  {
    value: 'system',
    label: 'System',
    hint: 'Follow the OS-level prefers-color-scheme setting.',
  },
];

export function ThemeSection() {
  const { mode, resolved, setMode } = useTheme();

  return (
    <Card>
      <CardHeader>
        <CardTitle>Theme</CardTitle>
        <CardDescription>
          Currently rendering in <span className="font-medium capitalize">{resolved}</span>{' '}
          mode. Stored locally — applies to this browser only.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <RadioGroup
          value={mode}
          onValueChange={(v) => setMode(v as ThemeMode)}
          className="space-y-3"
        >
          {OPTIONS.map((o) => (
            <div key={o.value} className="flex items-start gap-3">
              <RadioGroupItem id={`theme-${o.value}`} value={o.value} className="mt-0.5" />
              <div className="space-y-0.5">
                <Label htmlFor={`theme-${o.value}`} className="font-medium">
                  {o.label}
                </Label>
                <p className="text-xs text-muted-foreground">{o.hint}</p>
              </div>
            </div>
          ))}
        </RadioGroup>
      </CardContent>
    </Card>
  );
}
