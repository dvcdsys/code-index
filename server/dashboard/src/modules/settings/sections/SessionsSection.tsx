import { AlertCircle } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Alert, AlertDescription, AlertTitle } from '@/ui/alert';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/ui/card';
import { Skeleton } from '@/ui/skeleton';
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@/ui/table';
import { SessionRow } from '../components/SessionRow';
import { useMySessions } from '../hooks';

export function SessionsSection() {
  const { data, error, isLoading } = useMySessions();

  return (
    <Card>
      <CardHeader>
        <CardTitle>Active sessions</CardTitle>
        <CardDescription>
          Browsers signed in to your account. Sign out of any session you
          don&rsquo;t recognise.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : error ? (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertTitle>Failed to load sessions</AlertTitle>
            <AlertDescription>
              {error instanceof ApiError ? error.detail : String(error)}
            </AlertDescription>
          </Alert>
        ) : !data || data.sessions.length === 0 ? (
          <p className="text-sm text-muted-foreground">No active sessions.</p>
        ) : (
          <div className="rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Started</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead>IP</TableHead>
                  <TableHead>User agent</TableHead>
                  <TableHead className="w-20"></TableHead>
                  <TableHead className="w-28 text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.sessions.map((s) => (
                  <SessionRow key={s.id} session={s} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
