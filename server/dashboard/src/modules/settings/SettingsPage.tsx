import { EditorSection } from './sections/EditorSection';
import { ProfileSection } from './sections/ProfileSection';
import { SessionsSection } from './sections/SessionsSection';
import { ThemeSection } from './sections/ThemeSection';

export default function SettingsPage() {
  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Account, sessions, and personal UI preferences.
        </p>
      </header>

      <ProfileSection />
      <SessionsSection />
      <EditorSection />
      <ThemeSection />
    </div>
  );
}
