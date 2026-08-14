import { useState } from 'react';
import { Page } from '@/ui/page';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/ui/tabs';
import TokensTab from './TokensTab';
import WebhooksTab from './WebhooksTab';

type Tab = 'tokens' | 'webhooks';

// Two GitHub-facing concerns: the Personal Access Tokens used to clone
// private repositories and register hooks, and the delivery status of those
// hooks. The tunnel that supplies the public webhook URL is a separate page.
export default function GithubIntegrationPage() {
  const [tab, setTab] = useState<Tab>('tokens');

  return (
    <Page
      title="GitHub integration"
      subtitle="Tokens and webhook delivery for GitHub-backed projects."
    >
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          <TabsTrigger value="tokens">Tokens</TabsTrigger>
          <TabsTrigger value="webhooks">Webhooks</TabsTrigger>
        </TabsList>
        <TabsContent value="tokens">
          <TokensTab />
        </TabsContent>
        <TabsContent value="webhooks">
          <WebhooksTab />
        </TabsContent>
      </Tabs>
    </Page>
  );
}
