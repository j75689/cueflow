"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { type PostDraft, PostEditor, PostPreviews, emptyDraft } from "@/components/post-editor";
import { useMeta, useToast } from "@/components/providers";
import { Button, Card, CardHeader, Loading, PageHeader } from "@/components/ui";
import { api } from "@/lib/api";
import { useAsync } from "@/lib/hooks";

export default function NewPostPage() {
  const { meta } = useMeta();
  const toast = useToast();
  const router = useRouter();
  const publishers = useAsync(() => api.listConnectors("publisher"), []);
  const [draft, setDraft] = useState<PostDraft>({ ...emptyDraft, platforms: ["threads"] });
  const [busy, setBusy] = useState<"draft" | "submit" | null>(null);

  const valid = draft.content.trim().length > 0 && draft.platforms.length > 0;

  const create = async (submit: boolean) => {
    setBusy(submit ? "submit" : "draft");
    try {
      const p = await api.createPost({
        title: draft.title,
        content: draft.content,
        platforms: draft.platforms,
        thread_parts: draft.thread_parts.filter((x) => x.trim()),
        hashtags: draft.hashtags,
        media_urls: draft.media_urls.map((u) => u.trim()).filter(Boolean),
        image_prompt: draft.image_prompt,
        scheduled_at: draft.scheduled_at,
        publisher_connector_id: draft.publisher_connector_id,
        accounts: draft.accounts,
        topic: draft.topic,
        link_url: draft.link_url,
        submit,
      });
      toast.success(submit ? "Created and sent to review" : "Draft saved");
      router.push(`/posts/view/?id=${p.id}`);
    } catch (e) {
      toast.error(e);
      setBusy(null);
    }
  };

  if (!meta) return <Loading />;

  return (
    <div>
      <div className="mb-2">
        <Link href="/" className="text-xs text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
          ← Review
        </Link>
      </div>
      <PageHeader
        title="New draft"
        subtitle="Write a post by hand. It goes through the same review flow as agent-generated posts."
        actions={
          <>
            <Button onClick={() => create(false)} disabled={!valid} loading={busy === "draft"}>
              Save draft
            </Button>
            <Button variant="primary" onClick={() => create(true)} disabled={!valid} loading={busy === "submit"}>
              Create &amp; submit for review
            </Button>
          </>
        }
      />
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <PostEditor
          draft={draft}
          onChange={setDraft}
          platforms={meta.platforms}
          publishers={publishers.data ?? []}
          connectorTypes={meta.connector_types}
        />
        <div>
          <Card className="lg:sticky lg:top-4">
            <CardHeader title="Preview" />
            <PostPreviews draft={draft} platforms={meta.platforms} />
          </Card>
        </div>
      </div>
    </div>
  );
}
