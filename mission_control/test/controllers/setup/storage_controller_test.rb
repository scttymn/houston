require "test_helper"
require_relative "../../support/fake_docker"

class Setup::StorageControllerTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  RESTIC = "restic/restic:0.19.1"
  NFS = { kind: "nfs", name: "unas-nfs", nfs_server: "10.0.1.20", nfs_export: "/volume1/houston" }

  setup do
    StorageLocation.delete_all
    sign_in_as users(:one)
  end

  # Volumes don't exist yet unless a test says otherwise.
  def fresh_docker(&responder)
    FakeDocker.new do |args, env|
      next failure("Error: No such volume") if args[0..1] == %w[volume inspect]
      responder&.call(args, env)
    end
  end

  def submit(params)
    post setup_storage_path, params: { storage: params }
  end

  def restic_calls(fake) = fake.calls.select { |c| c.args.include?(RESTIC) }

  test "shows the storage form" do
    get setup_storage_path

    assert_response :success
    assert_select "h1", /Default backup storage/i
    %w[nfs local s3 b2].each { |kind| assert_select "input[type=radio][name='storage[kind]'][value=#{kind}]" }
    assert_select "input[type=radio][value=sftp]", 0
    %w[name nfs_server nfs_export local_path s3_endpoint s3_bucket s3_access_key_id s3_secret_access_key b2_bucket b2_key_id b2_application_key].each do |field|
      assert_select "input[name='storage[#{field}]']"
    end
  end

  test "an nfs location is created and initialized" do
    use_fake_docker(fresh_docker) do |fake|
      submit(NFS)

      assert_redirected_to setup_storage_path
      location = StorageLocation.find_by!(name: "unas-nfs")
      password = location.restic_password
      assert password.length >= 32

      assert_equal [
        %w[volume inspect houston-storage-unas-nfs],
        %w[volume create --driver local --opt type=nfs --opt o=addr=10.0.1.20,rw,nfsvers=4 --opt device=:/volume1/houston houston-storage-unas-nfs],
        [ "run", "--rm", "-e", "RESTIC_PASSWORD", "-e", "RESTIC_REPOSITORY", "-v", "houston-restic-cache:/root/.cache/restic", "-v", "houston-storage-unas-nfs:/repo", RESTIC, "init" ]
      ], fake.calls.map(&:args)
      assert_equal({ "RESTIC_PASSWORD" => password, "RESTIC_REPOSITORY" => "/repo" }, fake.calls.last.env)
      assert_not_includes fake.all_args.join(" "), password, "the password never goes on a command line"
      assert location.verified?
      raw = StorageLocation.connection.select_value("SELECT restic_password FROM storage_locations")
      assert_not_includes raw, password, "the password is stored encrypted"

      follow_redirect!
      assert_select ".check", /Wrote a test file/
      assert_select "#password", password
      assert_select "input[type=checkbox][name=saved]"
    end
  end

  test "each kind gets its repository and credentials" do
    cases = {
      local: [ { kind: "local", name: "backups", local_path: "/srv/houston-backups" },
               [ "-v", "/srv/houston-backups:/repo" ], { "RESTIC_REPOSITORY" => "/repo" } ],
      s3: [ { kind: "s3", name: "s3-offsite", s3_endpoint: "https://s3.us-west-2.amazonaws.com", s3_bucket: "houston-backups",
              s3_access_key_id: "AKIAEXAMPLE", s3_secret_access_key: "s3-secret-value" },
            [ "-e", "AWS_ACCESS_KEY_ID", "-e", "AWS_SECRET_ACCESS_KEY" ],
            { "RESTIC_REPOSITORY" => "s3:https://s3.us-west-2.amazonaws.com/houston-backups/houston",
              "AWS_ACCESS_KEY_ID" => "AKIAEXAMPLE", "AWS_SECRET_ACCESS_KEY" => "s3-secret-value" } ],
      b2: [ { kind: "b2", name: "b2-offsite", b2_bucket: "houston-backups", b2_key_id: "0012345", b2_application_key: "b2-secret-value" },
            [ "-e", "B2_ACCOUNT_ID", "-e", "B2_ACCOUNT_KEY" ],
            { "RESTIC_REPOSITORY" => "b2:houston-backups:houston", "B2_ACCOUNT_ID" => "0012345", "B2_ACCOUNT_KEY" => "b2-secret-value" } ]
    }
    cases.each do |kind, (params, args, env)|
      StorageLocation.delete_all
      use_fake_docker(fresh_docker) do |fake|
        submit(params)

        assert_redirected_to setup_storage_path, "#{kind} should succeed"
        init = restic_calls(fake).last
        assert_equal "init", init.args.last
        args.each_slice(2) { |pair| assert_includes init.args.each_cons(2).to_a, pair, "#{kind}: #{pair}" }
        env.each { |k, v| assert_equal v, init.env[k], "#{kind}: #{k}" }
        [ "s3-secret-value", "b2-secret-value", StorageLocation.last.restic_password ].each do |secret|
          assert_not_includes fake.all_args.join(" "), secret, "#{kind}: a secret reached argv"
        end
      end
    end
  end

  test "invalid details run nothing" do
    [
      NFS.merge(name: "Bad Name"),
      NFS.merge(nfs_server: "not a host!"),
      NFS.merge(nfs_export: "volume1/houston"),
      { kind: "local", name: "backups", local_path: "relative/path" },
      { kind: "s3", name: "s3-offsite", s3_bucket: "", s3_access_key_id: "a", s3_secret_access_key: "b" },
      { kind: "b2", name: "b2-offsite", b2_bucket: "houston-backups", b2_key_id: "", b2_application_key: "" },
      { kind: "sftp", name: "box" }
    ].each do |params|
      use_fake_docker(fresh_docker) do |fake|
        submit(params)

        assert_response :unprocessable_entity, "#{params} should be rejected"
        assert_empty fake.calls, "#{params} ran docker"
      end
    end
  end

  test "a failed init can be retried with the same password" do
    use_fake_docker(fresh_docker { |args, _| failure("Fatal: create repository at /repo failed: permission denied") if args.last == "init" }) do
      submit(NFS)
    end
    assert_response :unprocessable_entity
    assert_select ".check", /permission denied/
    first_password = StorageLocation.find_by!(name: "unas-nfs").restic_password
    assert_not StorageLocation.find_by!(name: "unas-nfs").verified?

    use_fake_docker(FakeDocker.new do |args, _|
      failure("Fatal: create key in repository at /repo failed: repository master key and config already initialized") if args.last == "init"
    end) do |fake|
      submit(NFS)

      assert_redirected_to setup_storage_path
      assert_equal %w[cat config], restic_calls(fake).last.args.last(2)
      assert(restic_calls(fake).all? { |c| c.env["RESTIC_PASSWORD"] == first_password }, "the rerun reuses the saved password")
      assert_not(fake.calls.any? { |c| c.args[0..1] == %w[volume create] }, "the existing volume is reused")
    end
    assert StorageLocation.find_by!(name: "unas-nfs").verified?
  end

  test "never takes over a repository it can't open" do
    use_fake_docker(fresh_docker do |args, _|
      case args.last(2)
      when %w[repo init], [ RESTIC, "init" ] then failure("Fatal: repository master key and config already initialized")
      when %w[cat config] then failure("Fatal: wrong password or no key found")
      end
    end) do |fake|
      submit(NFS)

      assert_response :unprocessable_entity
      assert_select ".check", /already a restic repository/
      assert_select ".check", /wrong password or no key found/, "restic's own words are shown"
      assert_not StorageLocation.find_by!(name: "unas-nfs").verified?
      assert_not(fake.all_args.any? { |a| %w[forget prune rm].include?(a) }, "nothing is removed")
    end
  end

  test "finishing needs the password saved and closes the step" do
    use_fake_docker(fresh_docker) { submit(NFS) }

    post finish_setup_storage_path, params: { saved: "0" }
    assert_response :unprocessable_entity
    assert_not StorageLocation.find_by!(name: "unas-nfs").acknowledged?

    post finish_setup_storage_path, params: { saved: "1" }
    assert_redirected_to root_path
    location = StorageLocation.find_by!(name: "unas-nfs")
    assert location.acknowledged?
    assert location.default?

    get setup_storage_path
    assert_redirected_to root_path
    get password_setup_storage_path(format: :txt)
    assert_redirected_to root_path
  end

  test "the password can be downloaded until setup finishes" do
    use_fake_docker(fresh_docker { |args, _| failure("permission denied") if args.last == "init" }) { submit(NFS) }
    get password_setup_storage_path(format: :txt)
    assert_response :not_found

    use_fake_docker(FakeDocker.new { |args, _| failure("already initialized") if args.last == "init" }) { submit(NFS) }
    get password_setup_storage_path(format: :txt)
    assert_response :success
    assert_equal "text/plain", response.media_type
    assert_match(/attachment/, response.headers["Content-Disposition"])
    assert_includes response.body, StorageLocation.find_by!(name: "unas-nfs").restic_password
    assert_includes response.headers["Cache-Control"], "no-store", "the download mustn't be cached"

    get setup_storage_path
    assert_includes response.headers["Cache-Control"], "no-store", "the page showing the password mustn't be cached"
  end
end
