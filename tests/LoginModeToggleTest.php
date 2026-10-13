<?php

declare(strict_types=1);

namespace App\Tests;

use PHPUnit\Framework\Attributes\Group;
use Symfony\Bundle\FrameworkBundle\Test\WebTestCase;
use Symfony\UX\LiveComponent\Test\InteractsWithLiveComponents;
use App\UserInterface\Http\Component\LoginUser;

#[Group('functional')]
class LoginModeToggleTest extends WebTestCase
{
    use InteractsWithLiveComponents;

    public function testLoginModeToggleFunctionality(): void
    {
        $testComponent = $this->createLiveComponent(LoginUser::class);
        /** @var LoginUser $component */
        $component = $testComponent->component();

        // Initial state - should be in email link mode
        $this->assertFalse($component->usePassword);
        $this->assertFalse($component->isSubmitted);
        $this->assertFalse($component->isSuccessful);

        $crawler = $testComponent->render();
        $this->assertCount(0, $crawler->crawler()->filter('input[name="login_user[password]"]'));
        $this->assertStringContainsString(
            'Zaloguj się hasłem',
            $crawler->crawler()
                ->filter('button[data-live-action-param="togglePassword"]')
                ->text()
        );

        // Toggle to password mode
        $testComponent->call('togglePassword');
        /** @var LoginUser $component */
        $component = $testComponent->component();
        $this->assertTrue($component->usePassword);
        $this->assertFalse($component->isSubmitted);
        $this->assertFalse($component->isSuccessful);

        $crawler = $testComponent->render();
        $this->assertCount(1, $crawler->crawler()->filter('input[name*="password"]'));
        $this->assertStringContainsString(
            'Zaloguj się linkiem email',
            $crawler->crawler()
                ->filter('button[data-live-action-param="togglePassword"]')
                ->text()
        );
        $this->assertStringContainsString('Zaloguj się', $crawler->crawler()->filter('button[type="submit"]')->text());

        // Toggle back to email link mode

        $testComponent->call('togglePassword');
        /** @var LoginUser $component */
        $component = $testComponent->component();
        $this->assertFalse($component->usePassword);

        $crawler = $testComponent->render();
        $this->assertCount(0, $crawler->crawler()->filter('input[name*="password"]'));
        $this->assertStringContainsString(
            'Zaloguj się hasłem',
            $crawler->crawler()
                ->filter('button[data-live-action-param="togglePassword"]')
                ->text()
        );
        $this->assertStringContainsString(
            'Wyślij link logowania',
            $crawler->crawler()
                ->filter('button[type="submit"]')
                ->text()
        );
    }

    public function testLoginModeToggleResetsFormState(): void
    {
        // Skip the form submission test for now and focus on the toggle functionality
        // The form submission requires complex mocking that's beyond the scope of this test
        $testComponent = $this->createLiveComponent(LoginUser::class);

        // Simulate a successful submission by setting the component state directly
        /** @var LoginUser $loginComponent */
        $loginComponent = $testComponent->component();
        $loginComponent->isSubmitted = true;
        $loginComponent->isSuccessful = true;
        $loginComponent->submittedEmail = 'test@example.com';

        // Verify the state is set correctly
        $this->assertTrue($loginComponent->isSubmitted);
        $this->assertTrue($loginComponent->isSuccessful);
        $this->assertEquals('test@example.com', $loginComponent->submittedEmail);

        // Toggle to password mode
        $testComponent = $testComponent->call('togglePassword');

        /** @var LoginUser $loginComponent */
        $loginComponent = $testComponent->component();
        // Should reset to normal form state
        $this->assertFalse($loginComponent->isSubmitted);
        $this->assertFalse($loginComponent->isSuccessful);
        $this->assertEquals('', $loginComponent->submittedEmail);
        $this->assertTrue($loginComponent->usePassword);
    }

    public function testLoginModeToggleButtonStyling(): void
    {
        $testComponent = $this->createLiveComponent(LoginUser::class);

        $renderedComponent = $testComponent->render();
        $toggleButton = $renderedComponent->crawler()
            ->filter('button[data-live-action-param="togglePassword"]');

        // Check that the button has the correct styling classes
        $this->assertStringContainsString('cursor-pointer', (string) $toggleButton->attr('class'));
        $this->assertStringContainsString('text-sm', (string) $toggleButton->attr('class'));
        $this->assertStringContainsString('font-bold', (string) $toggleButton->attr('class'));
    }

    public function testPasswordResetLinkOnlyVisibleInPasswordMode(): void
    {
        $testComponent = $this->createLiveComponent(LoginUser::class);

        // In email mode - should not show reset link
        $crawler = $testComponent->render();
        $this->assertCount(0, $crawler->crawler()->filter('a[href="/reset-password"]'));

        // Toggle to password mode - should show reset link
        $testComponent->call('togglePassword');
        $crawler = $testComponent->render();
        $this->assertCount(1, $crawler->crawler()->filter('a[href="/reset-password"]'));
        $this->assertStringContainsString(
            'Zapomniałeś hasła?',
            $crawler->crawler()
                ->filter('a[href="/reset-password"]')
                ->text()
        );

        // Check reset link styling
        $resetLink = $crawler->crawler()
            ->filter('a[href="/reset-password"]');
        $this->assertStringContainsString('cursor-not-allowed', (string) $resetLink->attr('class'));
        $this->assertStringContainsString('text-[10px]', (string) $resetLink->attr('class'));
    }

    public function testSubmitButtonTextChangesWithMode(): void
    {
        $testComponent = $this->createLiveComponent(LoginUser::class);

        // In email mode
        $crawler = $testComponent->render();
        $submitButton = $crawler->crawler()
            ->filter('button[type="submit"]');
        $this->assertStringContainsString('cursor-pointer', (string) $submitButton->attr('class'));
        $this->assertStringContainsString('Wyślij link logowania', $submitButton->text());

        // Toggle to password mode
        $testComponent->call('togglePassword');
        $crawler = $testComponent->render();
        $submitButton = $crawler->crawler()
            ->filter('button[type="submit"]');
        $this->assertStringContainsString('cursor-pointer', (string) $submitButton->attr('class'));
        $this->assertStringContainsString('Zaloguj się', $submitButton->text());
    }

    public function testLoginFormStructureIntegrity(): void
    {
        $testComponent = $this->createLiveComponent(LoginUser::class);

        $crawler = $testComponent->render();

        // The form submits through the LiveComponent "save" action
        $form = $crawler->crawler()
            ->filter('form');
        $this->assertCount(1, $form);
        $this->assertSame('live#action:prevent', $form->attr('data-action'));
        $this->assertSame('save', $form->attr('data-live-action-param'));

        // Email field lives in its own spaced container, and the password container is not rendered yet
        $emailContainer = $crawler->crawler()
            ->filter('form div.space-y-5:contains("Adres Email")');
        $this->assertCount(1, $emailContainer);
        $this->assertCount(1, $emailContainer->filter('input[name*="email"]'));
        $this->assertCount(0, $crawler->crawler()->filter('form div.mt-5:contains("Hasło")'));

        // Toggle to password mode and check password field structure
        $testComponent->call('togglePassword');
        $crawler = $testComponent->render();

        $passwordContainer = $crawler->crawler()
            ->filter('form div.mt-5:contains("Hasło")');
        $this->assertCount(1, $passwordContainer);
        $this->assertCount(1, $passwordContainer->filter('input[name*="password"]'));
    }
}
