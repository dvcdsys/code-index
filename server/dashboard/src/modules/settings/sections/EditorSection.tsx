import { useState } from 'react';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Label } from '@/ui/label';
import { RadioGroup, RadioGroupItem } from '@/ui/radio-group';
import {
  getEditorPreference,
  setEditorPreference,
  type EditorProtocol,
} from '@/lib/editorPreference';

const OPTIONS: ReadonlyArray<{
  value: EditorProtocol;
  label: string;
  hint: string;
}> = [
  {
    value: 'cursor',
    label: 'Cursor (default)',
    hint: 'Opens cursor:// — falls back to VS Code if Cursor is not installed.',
  },
  {
    value: 'vscode',
    label: 'VS Code',
    hint: 'Opens vscode:// directly.',
  },
  {
    value: 'none',
    label: 'Disabled',
    hint: 'The Open in editor button does nothing.',
  },
];

export function EditorSection() {
  const [pref, setPref] = useState<EditorProtocol>(() => getEditorPreference());

  function onChange(next: EditorProtocol) {
    setPref(next);
    setEditorPreference(next);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Open in editor</CardTitle>
        <CardDescription>
          What happens when you click the Open icon next to a search result.
          Stored locally — applies to this browser only.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <RadioGroup
          value={pref}
          onValueChange={(v) => onChange(v as EditorProtocol)}
          className="space-y-3"
        >
          {OPTIONS.map((o) => (
            <div key={o.value} className="flex items-start gap-3">
              <RadioGroupItem id={`editor-${o.value}`} value={o.value} className="mt-0.5" />
              <div className="space-y-0.5">
                <Label htmlFor={`editor-${o.value}`} className="font-medium">
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
