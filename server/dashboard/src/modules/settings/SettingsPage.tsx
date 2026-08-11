import { Page } from '@/ui/page';
import { EditorSection } from './sections/EditorSection';
import { ProfileSection } from './sections/ProfileSection';
import { SessionsSection } from './sections/SessionsSection';
import { ThemeSection } from './sections/ThemeSection';

// Personal, not administrative: everything here affects only the signed-in
// user (and, for theme and editor, only this browser).
export default function SettingsPage() {
  return (
    <Page title="Settings" subtitle="Your account, your sessions, and this browser's preferences.">
      <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_360px]">
        <div className="flex min-w-0 flex-col gap-5">
          <ProfileSection />
          <SessionsSection />
        </div>
        <div className="flex flex-col gap-5">
          <ThemeSection />
          <EditorSection />
        </div>
      </div>
    </Page>
  );
}
