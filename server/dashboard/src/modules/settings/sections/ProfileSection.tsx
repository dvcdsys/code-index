import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { useAuth } from '@/auth/useAuth';
import { ChangePasswordForm } from '../components/ChangePasswordForm';

export function ProfileSection() {
  const { user } = useAuth();
  return (
    <Card>
      <CardHeader>
        <CardTitle>Profile</CardTitle>
        <CardDescription>Account email + password.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid gap-1">
          <span className="text-xs uppercase tracking-wider text-muted-foreground">
            Email
          </span>
          <span className="font-medium">{user?.email ?? '—'}</span>
          <span className="text-xs text-muted-foreground capitalize">
            Role: {user?.role ?? 'unknown'}
          </span>
        </div>
        <div className="border-t pt-4">
          <h3 className="mb-3 text-sm font-medium">Change password</h3>
          <ChangePasswordForm />
        </div>
      </CardContent>
    </Card>
  );
}
