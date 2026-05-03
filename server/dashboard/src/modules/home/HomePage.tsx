import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';

// Welcome screen shown after login until the Projects + Search modules
// (PR-C) take over the home slot.
export default function HomePage() {
  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Welcome to cix</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The semantic code-index dashboard. More features land in upcoming releases.
        </p>
      </header>

      <div className="grid gap-4 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Projects</CardTitle>
            <CardDescription>Coming soon — manage indexed code repositories from the UI.</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            For now, register projects via the <code className="rounded bg-muted px-1 py-0.5">cix</code> CLI.
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Search</CardTitle>
            <CardDescription>Semantic, symbols, references — five modes, one UI.</CardDescription>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground">
            Use <code className="rounded bg-muted px-1 py-0.5">cix search "&hellip;"</code> from your terminal until the Search module ships.
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
