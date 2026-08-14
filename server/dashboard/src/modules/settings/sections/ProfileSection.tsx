import { Badge } from '@/ui/badge';
import { Card, CardBody, CardHead } from '@/ui/card';
import { useAuth } from '@/auth/useAuth';
import { ChangePasswordForm } from '../components/ChangePasswordForm';

export function ProfileSection() {
  const { user } = useAuth();
  return (
    <Card>
      <CardHead title="Profile" aside={<Badge variant="outline">{user?.role ?? 'unknown'}</Badge>} />
      <CardBody className="flex flex-col gap-5">
        <div>
          <span className="cix-label">Email</span>
          <div className="mt-1 font-mono text-[15px] font-semibold">{user?.email ?? '—'}</div>
        </div>
        <div className="border-t border-line-soft pt-5">
          <span className="cix-label">Change password</span>
          <div className="mt-3">
            <ChangePasswordForm />
          </div>
        </div>
      </CardBody>
    </Card>
  );
}
