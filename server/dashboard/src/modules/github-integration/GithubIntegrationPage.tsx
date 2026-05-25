import { useState } from 'react';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/ui/tabs';
import TokensTab from './TokensTab';
import WebhooksTab from './WebhooksTab';

type Tab = 'tokens' | 'webhooks';

// GithubIntegrationPage groups the GitHub-facing settings: Personal Access
// Tokens (used for cloning private repos + registering webhooks) and Webhook
// Integrations (auto-registration status + manual re-register). The tunnel
// that provides the public webhook URL is managed separately under
// Managed Tunnels.
export default function GithubIntegrationPage() {
  const [tab, setTab] = useState<Tab>('tokens');

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">GitHub Integration</h1>
        <p className="text-sm text-muted-foreground">
          Tokens and webhook delivery for GitHub-backed projects.
        </p>
      </header>

      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          <TabsTrigger value="tokens">Tokens</TabsTrigger>
          <TabsTrigger value="webhooks">Webhook Integrations</TabsTrigger>
        </TabsList>
        <TabsContent value="tokens" className="mt-4">
          <TokensTab />
        </TabsContent>
        <TabsContent value="webhooks" className="mt-4">
          <WebhooksTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}
